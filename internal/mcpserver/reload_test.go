package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
	"github.com/ryanhelms/agentflow/internal/config"
)

// configFor returns the repository configuration of gitRepo with its Jira
// site set to host, so a test can tell the configurations apart.
func configFor(host string) string {
	return "agentflow: 1\njira: {site: http://" + host + "}\ntask_management: {provider: jira}\nflow_map: flow-map.yml\npolicy:\n  max_autonomy: {local: auto, remote: confirm}\n"
}

// writeFile writes a file and moves its modification time on, so a rewrite
// within one clock tick is still a change on file systems with coarse
// timestamps.
func writeFile(t *testing.T, path, content string) {
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

// reloadFixture is an MCP server on a fixture repository, with its client.
type reloadFixture struct {
	dir    string
	config string
	home   string
	s      *Server
	cs     *mcp.ClientSession
}

func newReloadFixture(t *testing.T, tools func(*Server)) *reloadFixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	t.Setenv("JIRA_USER", "fixture")
	t.Setenv("JIRA_TOKEN", "fixture")
	t.Setenv("GH_TOKEN", "x")
	previous := http.DefaultTransport
	http.DefaultTransport = jiraUnavailable{}
	t.Cleanup(func() { http.DefaultTransport = previous })
	dir := gitRepo(t)
	f := &reloadFixture{dir: dir, config: filepath.Join(dir, ".agentflow", "config.yml"), home: home}
	writeFile(t, f.config, configFor("127.0.0.1"))
	ctx := t.Context()
	s, err := New(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if tools != nil {
		tools(s)
	}
	f.s = s
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(ctx, st) }()
	f.cs, err = mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.cs.Close() })
	return f
}

// call runs a tool and returns the text of each content block.
func (f *reloadFixture) call(t *testing.T, name string, args map[string]any) []string {
	t.Helper()
	res, err := f.cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var texts []string
	for _, c := range res.Content {
		texts = append(texts, c.(*mcp.TextContent).Text)
	}
	return texts
}

func (f *reloadFixture) site() string { return f.s.app.Load().Cfg.Jira.Site }

// noticeText returns the notices in a result: every block after the first.
func noticeText(texts []string) string { return strings.Join(texts[1:], "\n") }

func TestAChangedConfigurationIsPickedUpBeforeTheNextCallWithoutADaemon(t *testing.T) {
	f := newReloadFixture(t, nil)
	first := f.s.app.Load()
	if out := f.call(t, "agentflow_policy_explain", nil); len(out) != 1 {
		t.Fatalf("a first call had notices: %v", out[1:])
	}
	if f.s.app.Load() != first {
		t.Fatal("a call without a change replaced the App")
	}

	// A repository edit.
	writeFile(t, f.config, configFor("127.0.0.22"))
	out := f.call(t, "agentflow_policy_explain", nil)
	if f.site() != "http://127.0.0.22" || f.s.app.Load() == first {
		t.Fatalf("site %q after the edit", f.site())
	}
	if !strings.Contains(noticeText(out), "reloaded its configuration") {
		t.Fatalf("no reload notice with the call that reloaded: %v", out)
	}
	if out := f.call(t, "agentflow_policy_explain", nil); len(out) != 1 {
		t.Fatalf("the notice came twice: %v", out[1:])
	}

	// A personal edit: the next call runs with it.
	writeFile(t, filepath.Join(f.home, "config.yml"), "autonomy: {local: auto, remote: confirm}\n")
	f.call(t, "agentflow_policy_explain", nil)
	if got := f.s.app.Load().User.Autonomy.Local; got != config.Auto {
		t.Fatalf("personal local autonomy = %q after the edit", got)
	}

	// A provider definition.
	writeFile(t, filepath.Join(f.home, "providers", "later.yml"), "version: 1\nproviders: {jira: {task_management: {settings: {site: https://later.example}}}}")
	if out := f.call(t, "agentflow_setup_providers", map[string]any{"explain": true}); !strings.Contains(out[0], "later.example") {
		t.Fatalf("a new provider definition was not used: %s", out[0])
	}
}

func TestACallInFlightFinishesOnTheConfigurationItStartedWith(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	f := newReloadFixture(t, func(s *Server) {
		add(s, "test_hold", "waits, then reports the site of the App it runs on", readOnly,
			func(_ context.Context, a *app.App, _ noIn) (any, error) {
				started <- struct{}{}
				<-release
				return a.Cfg.Jira.Site, nil
			})
	})
	held := make(chan []string, 1)
	go func() { held <- f.call(t, "test_hold", nil) }()
	<-started

	writeFile(t, f.config, configFor("127.0.0.33"))
	out := f.call(t, "agentflow_policy_explain", nil) // reloads while test_hold is in flight
	if f.site() != "http://127.0.0.33" || !strings.Contains(noticeText(out), "reloaded") {
		t.Fatalf("the second call did not reload: %q, %v", f.site(), out)
	}
	close(release)
	if got := <-held; !strings.Contains(got[0], "http://127.0.0.1") || strings.Contains(got[0], "127.0.0.33") {
		t.Fatalf("the call in flight switched configuration: %v", got)
	}
	// The next call starts on the new one.
	go func() { <-started }()
	if got := f.call(t, "test_hold", nil); !strings.Contains(got[0], "http://127.0.0.33") {
		t.Fatalf("a new call used the old configuration: %v", got)
	}
}

func TestAConfigurationThatDoesNotLoadKeepsTheOldOneAndSaysSoOnce(t *testing.T) {
	f := newReloadFixture(t, nil)
	before := f.s.app.Load()
	writeFile(t, f.config, "agentflow: [\n")
	out := f.call(t, "agentflow_policy_explain", nil)
	if !strings.Contains(noticeText(out), "could not reload") || !strings.Contains(noticeText(out), "keeps the settings from before") {
		t.Fatalf("no failure notice: %v", out)
	}
	if f.s.app.Load() != before {
		t.Fatal("a failed reload replaced the App")
	}
	if out := f.call(t, "agentflow_policy_explain", nil); len(out) != 1 {
		t.Fatalf("the same failure was reported again: %v", out[1:])
	}
	if f.s.app.Load() != before {
		t.Fatal("the failed file was loaded on a later call")
	}

	// Fixing the file recovers without a restart.
	writeFile(t, f.config, configFor("127.0.0.44"))
	out = f.call(t, "agentflow_policy_explain", nil)
	if f.site() != "http://127.0.0.44" || !strings.Contains(noticeText(out), "reloaded") {
		t.Fatalf("the fixed file was not loaded: %q, %v", f.site(), out)
	}
}

func TestAProviderChangeTellsTheAgentToRestart(t *testing.T) {
	f := newReloadFixture(t, nil)
	writeFile(t, f.config, "agentflow: 1\ntask_management: {provider: none}\nflow_map: flow-map.yml\n")
	out := f.call(t, "agentflow_policy_explain", nil)
	if got := noticeText(out); !strings.Contains(got, "reloaded") || !strings.Contains(got, "restart") {
		t.Fatalf("notices = %q, want a reload notice and a restart notice", got)
	}
	if got := f.s.app.Load().Cfg.Providers.TaskManagement.Provider; got != "none" {
		t.Fatalf("the new provider is not in use: %q", got)
	}
	// The instructions are the ones the session started with.
	if f.s.reload.instructions != instructionsFor(f.s.app.Load()) && !strings.Contains(f.s.reload.instructions, "task_management=jira") {
		t.Fatalf("the start instructions changed: %q", f.s.reload.instructions)
	}
}

func TestARecoveryModeServerStaysInRecoveryUntilRestart(t *testing.T) {
	dir := gitRepo(t)
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	config := filepath.Join(dir, ".agentflow", "config.yml")
	writeFile(t, config, "agentflow: [\n")
	ctx := t.Context()
	s, err := New(ctx, dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	if s.reload != nil {
		t.Fatal("a server in recovery mode reloads")
	}
	recovery := s.app.Load()
	writeFile(t, config, configFor("127.0.0.1"))
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(ctx, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if _, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_config", Arguments: map[string]any{"operation": "list"}}); err != nil {
		t.Fatal(err)
	}
	if s.app.Load() != recovery {
		t.Fatal("recovery mode switched to a full App after the file was repaired")
	}
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 2 {
		t.Fatalf("recovery tools = %v, %v", list, err)
	}
}

// sharedStore lets the server open a fake store as often as it likes.
type sharedStore struct{ *storetest.Store }

func (sharedStore) Close() error { return nil }

// fakeDaemonStores stands in for the stores a running daemon owns.
func fakeDaemonStores(t *testing.T, repo bus.RepoID) (repoStore, globalStore *storetest.Store) {
	t.Helper()
	repoStore, globalStore = storetest.New(repo), storetest.New(bus.GlobalRepo)
	oldRepo, oldGlobal, oldRetry := app.OpenEventStore, app.OpenGlobalEventStore, app.ReloadRetry
	app.OpenEventStore = func(_ context.Context, _ string, role store.Role) (store.Store, error) {
		if role != store.RoleReader {
			t.Errorf("the MCP server opened an event store as %v", role)
		}
		return sharedStore{repoStore}, nil
	}
	app.OpenGlobalEventStore = func(_ context.Context, role store.Role) (store.Store, error) {
		if role != store.RoleReader {
			t.Errorf("the MCP server opened the global store as %v", role)
		}
		return sharedStore{globalStore}, nil
	}
	app.ReloadRetry = 20 * time.Millisecond
	t.Cleanup(func() { app.OpenEventStore, app.OpenGlobalEventStore, app.ReloadRetry = oldRepo, oldGlobal, oldRetry })
	return repoStore, globalStore
}

func configEvent(repo bus.RepoID, worktree bus.WorktreeID, layer string) bus.Event {
	return bus.Event{Type: bus.TypeConfigLayerChanged, Source: "watch.config", Repo: repo, Worktree: worktree, Payload: json.RawMessage(`{"layer":"` + layer + `"}`)}
}

func put(t *testing.T, st *storetest.Store, e bus.Event) {
	t.Helper()
	if _, err := st.Append(context.Background(), e.Normalize(time.Now())); err != nil {
		t.Fatal(err)
	}
}

// eventually runs try until check holds. A follower starts at the end of the
// stores, so an event sent before it attached is lost; try runs again.
func eventually(t *testing.T, what string, try func(), check func() bool) {
	t.Helper()
	for range 12 {
		try()
		deadline := time.Now().Add(2 * app.ReloadDebounce)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAConfigEventReloadsTheServerWithoutAToolCall(t *testing.T) {
	const repoID bus.RepoID = "0123456789abcdef"
	repoStore, globalStore := fakeDaemonStores(t, repoID)
	f := newReloadFixture(t, nil)
	ctx, cancel := context.WithCancel(t.Context())
	wait := f.s.startReload(ctx)
	t.Cleanup(func() {
		cancel()
		wait() // before the fake stores are put back
	})

	// An edit and its repository-layer event: no tool call is made.
	n := 50
	eventually(t, "the repository layer event to reload", func() {
		n++
		writeFile(t, f.config, configFor("127.0.0."+strconv.Itoa(n)))
		put(t, repoStore, configEvent(repoID, "", "repo"))
	}, func() bool { return f.site() == "http://127.0.0."+strconv.Itoa(n) })

	// The call after the event carries the notice, and the files are not
	// loaded a second time: the fingerprint was recorded with the reload.
	applied := f.s.app.Load()
	out := f.call(t, "agentflow_policy_explain", nil)
	if !strings.Contains(noticeText(out), "reloaded") || f.s.app.Load() != applied {
		t.Fatalf("after the event: %v, App replaced: %t", out, f.s.app.Load() != applied)
	}

	// An event for another worktree is not this server's business.
	writeFile(t, f.config, configFor("127.0.0.99"))
	put(t, repoStore, configEvent(repoID, "other-0badc0de", "repo"))
	time.Sleep(3 * app.ReloadDebounce)
	if f.s.app.Load() != applied {
		t.Fatal("an event for another worktree reloaded the server")
	}

	// The personal layer comes from the global store.
	writeFile(t, filepath.Join(f.home, "config.yml"), "autonomy: {local: auto, remote: confirm}\n")
	eventually(t, "the personal layer event to reload", func() {
		put(t, globalStore, configEvent(bus.GlobalRepo, "", "personal"))
	}, func() bool { return f.s.app.Load().User.Autonomy.Local == config.Auto })
	if f.site() != "http://127.0.0.99" {
		t.Fatalf("the reload did not read the repository edit made earlier: %q", f.site())
	}
}

func TestAConfirmationTokenSurvivesAReload(t *testing.T) {
	f := newReloadFixture(t, nil)
	// A personal confirm level for local actions: the first commit call asks.
	writeFile(t, filepath.Join(f.home, "config.yml"), "autonomy: {local: confirm, remote: confirm}\n")
	args := map[string]any{"message": "feat(PLAT-123): add quota package", "paths": []string{"quota.go"}}
	var pending struct {
		Status string `json:"status"`
		Token  string `json:"confirm_token"`
	}
	if err := json.Unmarshal([]byte(f.call(t, "agentflow_commit", args)[0]), &pending); err != nil || pending.Status != "needs_confirmation" || pending.Token == "" {
		t.Fatalf("first call = %+v, %v", pending, err)
	}

	// The configuration changes (unrelated to this action), then the user agrees.
	writeFile(t, f.config, configFor("127.0.0.77"))
	args["confirm_token"] = pending.Token
	out := f.call(t, "agentflow_commit", args)
	var done struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(out[0]), &done); err != nil || done.Status != "committed" {
		t.Fatalf("after the reload: %v", out)
	}
	if f.site() != "http://127.0.0.77" {
		t.Fatal("the commit call did not reload first")
	}
}
