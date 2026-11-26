package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
)

func TestNeedsReload(t *testing.T) {
	const repo, other = bus.RepoID("0123456789abcdef"), bus.RepoID("fedcba9876543210")
	ev := func(r bus.RepoID, w bus.WorktreeID, typ bus.Type, payload string) bus.Event {
		return bus.Event{Type: typ, Repo: r, Worktree: w, Payload: []byte(payload)}
	}
	cases := []struct {
		name string
		e    bus.Event
		want bool
	}{
		{"own repo layer", ev(repo, "main", bus.TypeConfigLayerChanged, `{"layer":"repo"}`), true},
		{"other worktree", ev(repo, "feature-0badc0de", bus.TypeConfigLayerChanged, `{"layer":"repo"}`), false},
		{"other repository", ev(other, "main", bus.TypeConfigLayerChanged, `{"layer":"repo"}`), false},
		{"personal layer", ev(bus.GlobalRepo, "", bus.TypeConfigLayerChanged, `{"layer":"personal"}`), true},
		{"personal layer outside global", ev(repo, "main", bus.TypeConfigLayerChanged, `{"layer":"personal"}`), false},
		{"client settings", ev(bus.GlobalRepo, "", bus.TypeClientSettingsChanged, `{"client":"claude"}`), false},
		{"git event", ev(repo, "main", bus.TypeGitHeadChanged, `{}`), false},
	}
	for _, c := range cases {
		if got := NeedsReload(c.e, repo, "main"); got != c.want {
			t.Errorf("%s: NeedsReload = %v", c.name, got)
		}
	}
	for _, topic := range ReloadTopics(repo, "main") {
		if !topic.Types.Match(bus.TypeConfigLayerChanged) || topic.Types.Match(bus.TypeGitHeadChanged) {
			t.Errorf("topic %v", topic)
		}
	}
}

// writeConfig writes path and moves its modification time on, so a rewrite
// within one clock tick still looks like a change on file systems with
// coarse timestamps.
func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	bump := time.Now().Add(time.Duration(len(content)+1) * time.Hour)
	if err := os.Chtimes(path, bump, bump); err != nil {
		t.Fatal(err)
	}
}

func fingerprintOf(t *testing.T, repo string) string {
	t.Helper()
	fp, err := ConfigFingerprint(repo)
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestConfigFingerprintFollowsTheConfigurationFiles(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	config := filepath.Join(repo, ".agentflow", "config.yml")
	writeConfig(t, config, "agentflow: 1\n")
	base := fingerprintOf(t, repo)
	if fingerprintOf(t, repo) != base {
		t.Fatal("the fingerprint of untouched files changed")
	}
	// Each of these is a change the MCP server must notice.
	steps := []struct {
		name string
		do   func()
	}{
		{"repository config edited", func() { writeConfig(t, config, "agentflow: 1\nupdate: {}\n") }},
		{"repository config replaced with the same size", func() {
			if err := os.Remove(config); err != nil {
				t.Fatal(err)
			}
			writeConfig(t, config, "agentflow: 1\nupdate: {}\n")
		}},
		{"repository provider added", func() { writeConfig(t, filepath.Join(repo, ".agentflow", "providers", "a.yml"), "version: 1\n") }},
		{"repository provider edited", func() {
			writeConfig(t, filepath.Join(repo, ".agentflow", "providers", "a.yml"), "version: 1\nproviders: {}\n")
		}},
		{"repository provider removed", func() { _ = os.Remove(filepath.Join(repo, ".agentflow", "providers", "a.yml")) }},
		{"personal config added", func() { writeConfig(t, filepath.Join(home, "config.yml"), "autonomy: {local: auto}\n") }},
		{"personal provider added", func() { writeConfig(t, filepath.Join(home, "providers", "p.yaml"), "version: 1\n") }},
		{"legacy repository config added", func() { writeConfig(t, filepath.Join(repo, ".devflow", "config.yml"), "agentflow: 1\n") }},
	}
	previous := fingerprintOf(t, repo)
	if previous != base {
		t.Fatal("fingerprint moved without a change")
	}
	for _, step := range steps {
		step.do()
		now := fingerprintOf(t, repo)
		if now == previous {
			t.Errorf("%s: the fingerprint did not change", step.name)
		}
		previous = now
	}
	// Files the loader ignores do not matter.
	writeConfig(t, filepath.Join(repo, ".agentflow", "providers", "notes.txt"), "x")
	writeConfig(t, filepath.Join(repo, ".agentflow", "releases.yml"), "units: []\n")
	if fingerprintOf(t, repo) != previous {
		t.Error("a file outside the configuration changed the fingerprint")
	}
}

// reloadRepo is a git repository with a configuration, opened as an App.
func reloadRepo(t *testing.T) (*App, string) {
	t.Helper()
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	config := filepath.Join(dir, ".agentflow", "config.yml")
	writeConfig(t, config, "agentflow: 1\njira: {site: https://one.example}\ntask_management: {provider: jira}\n")
	a, err := Open(context.Background(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	return a, config
}

func TestReloadBuildsANewAppAndLeavesTheOldOneAlone(t *testing.T) {
	a, config := reloadRepo(t)
	writeConfig(t, config, "agentflow: 1\njira: {site: https://two.example}\ntask_management: {provider: jira}\n")
	next, err := a.Reload(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if a.Cfg.Jira.Site != "https://one.example" || next.Cfg.Jira.Site != "https://two.example" {
		t.Fatalf("sites: old %q, new %q", a.Cfg.Jira.Site, next.Cfg.Jira.Site)
	}
	if next == a || next.Gate == a.Gate || next.Repo != a.Repo || next.Client != a.Client || next.Home != a.Home || !next.HasCfg {
		t.Fatalf("reload did not keep the identity: %+v", next)
	}
	// The gate keeps the approval key and the owner of the session: a token
	// the old gate made is still the same operation to the new one.
	if next.Gate.Client != a.Gate.Client {
		t.Fatalf("gate client %q, want %q", next.Gate.Client, a.Gate.Client)
	}

	// A file that does not load: the error says so and nothing changes.
	writeConfig(t, config, "agentflow: [\n")
	if bad, err := next.Reload(context.Background()); err == nil || bad != nil {
		t.Fatalf("a broken file reloaded: %v, %v", bad, err)
	} else {
		var load *ConfigLoadError
		if !errors.As(err, &load) {
			t.Fatalf("err = %T %v, want *ConfigLoadError", err, err)
		}
	}
	if next.Cfg.Jira.Site != "https://two.example" {
		t.Fatal("a failed reload changed the App")
	}

	// A session that started in recovery mode has nothing to reload.
	if _, err := (&App{Repo: a.Repo}).Reload(context.Background()); err == nil {
		t.Fatal("recovery mode reloaded")
	}
}

// configEvent is a config.layer.changed event of repo and worktree.
func configEvent(repo bus.RepoID, worktree bus.WorktreeID, layer string) bus.Event {
	return bus.Event{Type: bus.TypeConfigLayerChanged, Source: "watch.config", Repo: repo, Worktree: worktree, Payload: json.RawMessage(`{"layer":"` + layer + `"}`)}
}

func putEvent(t *testing.T, st *storetest.Store, e bus.Event) {
	t.Helper()
	if _, err := st.Append(context.Background(), e.Normalize(time.Now())); err != nil {
		t.Fatal(err)
	}
}

type changeCounter struct{ n atomic.Int32 }

func (c *changeCounter) call() { c.n.Add(1) }

func startFollowing(t *testing.T, a *App, worktree bus.WorktreeID, c *changeCounter) {
	t.Helper()
	old := ReloadRetry
	ReloadRetry = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		a.FollowConfigEvents(ctx, worktree, c.call)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		ReloadRetry = old
	})
}

// settle waits until an event sent by send makes the follower call changed.
// A follower starts at the end of the stores, so an event sent before it
// attached is lost: send again, after the debounce has had time to fire.
func settle(t *testing.T, c *changeCounter, send func()) {
	t.Helper()
	for range 8 {
		before := c.n.Load()
		send()
		time.Sleep(3 * ReloadDebounce)
		if c.n.Load() > before {
			return
		}
	}
	t.Fatal("the follower never called changed")
}

func TestFollowConfigEventsCallsOnceForABurstOfOwnEvents(t *testing.T) {
	a, _ := eventsApp(t, nil)
	set := newStoreSet(t)
	repo := set.addRepo(commonOf(t, a), eventsRepo)
	global := set.addGlobal()
	var c changeCounter
	startFollowing(t, a, "main", &c)
	settle(t, &c, func() { putEvent(t, repo, configEvent(eventsRepo, "main", "repo")) })
	settled := c.n.Load()

	// Events that are not this server's: another worktree, other types.
	putEvent(t, repo, configEvent(eventsRepo, "other-0badc0de", "repo"))
	putEvent(t, repo, bus.Event{Type: bus.TypeGitHeadChanged, Source: "test", Repo: eventsRepo, Worktree: "main"})
	putEvent(t, global, bus.Event{Type: bus.TypeClientSettingsChanged, Source: "test", Repo: bus.GlobalRepo, Payload: json.RawMessage(`{"client":"claude"}`)})
	time.Sleep(2 * ReloadDebounce)
	if got := c.n.Load(); got != settled {
		t.Fatalf("an event for another worktree or type made %d reloads", got-settled)
	}

	// A burst, 20 ms apart: five repository events and a personal one.
	for range 5 {
		putEvent(t, repo, configEvent(eventsRepo, "", "repo"))
		time.Sleep(20 * time.Millisecond)
	}
	putEvent(t, global, configEvent(bus.GlobalRepo, "", "personal"))
	time.Sleep(3 * ReloadDebounce)
	if got := c.n.Load(); got != settled+1 {
		t.Fatalf("a burst made %d reloads, want 1", got-settled)
	}
	set.onlyReaders(t)
}

func TestFollowConfigEventsWaitsForTheDaemonsStores(t *testing.T) {
	a, _ := eventsApp(t, nil)
	set := newStoreSet(t)
	// No daemon yet: nothing exists to follow, and nothing is started.
	var c changeCounter
	startFollowing(t, a, "main", &c)
	time.Sleep(100 * time.Millisecond)
	if c.n.Load() != 0 {
		t.Fatal("reloaded without any event")
	}
	// The daemon starts and creates the stores; the follower finds them.
	global := set.addGlobal()
	set.addRepo(commonOf(t, a), eventsRepo)
	settle(t, &c, func() { putEvent(t, global, configEvent(bus.GlobalRepo, "", "personal")) })
	set.onlyReaders(t)
}
