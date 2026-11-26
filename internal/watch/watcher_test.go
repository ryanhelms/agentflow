package watch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
	"github.com/ryanhelms/agentflow/internal/watch"
	"github.com/ryanhelms/agentflow/internal/watch/watchtest"
)

const testRepo bus.RepoID = "0123456789abcdef"

var modes = []watch.Mode{watch.ModeNotify, watch.ModePoll}

// isolateGit keeps the user's git configuration and hooks out of the test.
func isolateGit(t *testing.T) {
	t.Helper()
	empty := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": empty, "GIT_CONFIG_NOSYSTEM": "1", "GIT_EDITOR": "true",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com",
		"GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
		"AGENTFLOW_WATCH": "",
	} {
		t.Setenv(k, v)
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitErr(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return out
}

func gitErr(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo returns a repository with one commit on main.
func newRepo(t *testing.T) string {
	t.Helper()
	isolateGit(t)
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, root, "init", "-q", "-b", "main")
	writeFile(t, filepath.Join(root, "a.txt"), "base\n")
	git(t, root, "add", "a.txt")
	git(t, root, "commit", "-q", "-m", "base")
	return root
}

func target(t *testing.T, root string, wt bus.WorktreeID) watch.Target {
	t.Helper()
	tg, err := watch.NewTarget(context.Background(), testRepo, wt, root)
	if err != nil {
		t.Fatal(err)
	}
	return tg
}

type harness struct {
	w    *watch.FileWatcher
	sub  bus.Subscription
	seen []bus.Event
	// backlog holds events seen but not yet matched by wait.
	backlog []bus.Event

	mu   sync.Mutex
	errs []error
}

func start(t *testing.T, opts watch.Options, targets ...watch.Target) *harness {
	t.Helper()
	if opts.PollInterval == 0 {
		opts.PollInterval = 50 * time.Millisecond
		opts.PollRefsInterval = 100 * time.Millisecond
	}
	h := &harness{}
	opts.OnError = func(err error) { h.mu.Lock(); h.errs = append(h.errs, err); h.mu.Unlock() }
	w, err := watch.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	h.w = w
	for _, tg := range targets {
		if err := w.Add(tg); err != nil {
			t.Fatal(err)
		}
	}
	b := bustest.New(bus.Options{})
	all, err := bus.ParseTopic("*/*/**")
	if err != nil {
		t.Fatal(err)
	}
	h.sub, err = b.Subscribe(bus.SubscribeOptions{Name: "test", Buffer: 1024}, all)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, b) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
		_ = b.Close()
	})
	return h
}

// wait returns the first event matching pred, failing after 5 s.
func (h *harness) wait(t *testing.T, desc string, pred func(bus.Event) bool) bus.Event {
	t.Helper()
	for i, e := range h.backlog {
		if pred(e) {
			h.backlog = append(h.backlog[:i:i], h.backlog[i+1:]...)
			return e
		}
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-h.sub.Events():
			h.seen = append(h.seen, e)
			if pred(e) {
				return e
			}
			h.backlog = append(h.backlog, e)
		case <-deadline:
			var got []string
			for _, e := range h.seen {
				got = append(got, fmt.Sprintf("%s %s %s", e.Type, e.Worktree, e.Payload))
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			t.Fatalf("no %s; saw:\n%s\nerrors: %v", desc, strings.Join(got, "\n"), h.errs)
		}
	}
}

// quiet collects every event for d.
func (h *harness) quiet(d time.Duration) []bus.Event {
	var out []bus.Event
	deadline := time.After(d)
	for {
		select {
		case e := <-h.sub.Events():
			out = append(out, e)
		case <-deadline:
			return out
		}
	}
}

func payload[T any](t *testing.T, e bus.Event) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func is(typ bus.Type, wt bus.WorktreeID) func(bus.Event) bool {
	return func(e bus.Event) bool { return e.Type == typ && e.Worktree == wt }
}

func refsInclude(t *testing.T, ref string) func(bus.Event) bool {
	return func(e bus.Event) bool {
		return e.Type == bus.TypeGitRefsChanged && slices.Contains(payload[bus.GitRefsChanged](t, e).Refs, ref)
	}
}

func TestCommitCheckoutAndBranches(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			root := newRepo(t)
			main := target(t, root, "main")
			h := start(t, watch.Options{Mode: mode}, main)
			if m, _ := h.w.Mode(testRepo, "main"); m != mode {
				t.Fatalf("mode = %s", m)
			}
			before := git(t, root, "rev-parse", "HEAD")

			writeFile(t, filepath.Join(root, "a.txt"), "two\n")
			git(t, root, "commit", "-q", "-am", "two")
			after := git(t, root, "rev-parse", "HEAD")
			e := h.wait(t, "head moved by commit", is(bus.TypeGitHeadChanged, "main"))
			p := payload[bus.GitHeadChanged](t, e)
			if p.Ref != "refs/heads/main" || p.OID != after || p.PreviousOID != before || p.PreviousRef != "refs/heads/main" {
				t.Fatalf("head payload = %+v", p)
			}
			if e.CausationID == "" || e.Source != watch.SourceGit || e.Seq != 0 {
				t.Fatalf("head envelope = %+v", e)
			}
			h.wait(t, "refs/heads/main changed", refsInclude(t, "refs/heads/main"))
			h.wait(t, "index changed", is(bus.TypeGitIndexChanged, "main"))

			git(t, root, "branch", "feat")
			h.wait(t, "branch created", refsInclude(t, "refs/heads/feat"))

			git(t, root, "checkout", "-q", "feat")
			e = h.wait(t, "checkout", is(bus.TypeGitHeadChanged, "main"))
			if p := payload[bus.GitHeadChanged](t, e); p.Ref != "refs/heads/feat" || p.PreviousRef != "refs/heads/main" || p.OID != after {
				t.Fatalf("checkout payload = %+v", p)
			}

			git(t, root, "checkout", "-q", "--detach")
			e = h.wait(t, "detach", is(bus.TypeGitHeadChanged, "main"))
			if p := payload[bus.GitHeadChanged](t, e); p.Ref != "" || p.PreviousRef != "refs/heads/feat" {
				t.Fatalf("detach payload = %+v", p)
			}

			git(t, root, "checkout", "-q", "main")
			git(t, root, "branch", "-q", "-D", "feat")
			h.wait(t, "branch deleted", refsInclude(t, "refs/heads/feat"))

			git(t, root, "pack-refs", "--all")
			h.wait(t, "packed-refs rescan", func(e bus.Event) bool {
				return e.Type == bus.TypeGitRefsChanged && len(payload[bus.GitRefsChanged](t, e).Refs) == 0
			})
		})
	}
}

func TestRebaseStartAndAbort(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			root := newRepo(t)
			git(t, root, "checkout", "-q", "-b", "topic")
			writeFile(t, filepath.Join(root, "a.txt"), "topic\n")
			git(t, root, "commit", "-q", "-am", "topic")
			git(t, root, "checkout", "-q", "main")
			writeFile(t, filepath.Join(root, "a.txt"), "main\n")
			git(t, root, "commit", "-q", "-am", "main")
			git(t, root, "checkout", "-q", "topic")

			h := start(t, watch.Options{Mode: mode}, target(t, root, "main"))
			if _, err := gitErr(root, "rebase", "main"); err == nil {
				t.Fatal("rebase did not stop on the conflict")
			}
			e := h.wait(t, "rebase started", is(bus.TypeGitOperationChanged, "main"))
			if p := payload[bus.GitOperationChanged](t, e); p.Operation != bus.OperationRebase || p.Previous != bus.OperationNone {
				t.Fatalf("start payload = %+v", p)
			}
			git(t, root, "rebase", "--abort")
			e = h.wait(t, "rebase aborted", is(bus.TypeGitOperationChanged, "main"))
			if p := payload[bus.GitOperationChanged](t, e); p.Operation != bus.OperationNone || p.Previous != bus.OperationRebase {
				t.Fatalf("abort payload = %+v", p)
			}
			e = h.wait(t, "back on topic", func(e bus.Event) bool {
				return e.Type == bus.TypeGitHeadChanged && payload[bus.GitHeadChanged](t, e).Ref == "refs/heads/topic"
			})
			if e.Worktree != "main" {
				t.Fatalf("worktree = %q", e.Worktree)
			}
		})
	}
}

func TestLinkedWorktrees(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			root := newRepo(t)
			main := target(t, root, "main")
			h := start(t, watch.Options{Mode: mode}, main)

			wtRoot := filepath.Join(filepath.Dir(root), "linked")
			git(t, root, "worktree", "add", "-q", "-b", "linked", wtRoot)
			h.wait(t, "worktree added", func(e bus.Event) bool { return e.Type == bus.TypeGitWorktreesChanged })

			linked := target(t, wtRoot, "linked-1a2b3c4d")
			if linked.GitDir == linked.CommonDir || linked.CommonDir != main.CommonDir ||
				filepath.Dir(linked.GitDir) != filepath.Join(main.CommonDir, "worktrees") {
				t.Fatalf("linked target = %+v", linked)
			}
			if err := h.w.Add(linked); err != nil {
				t.Fatal(err)
			}
			if err := h.w.Add(linked); err == nil {
				t.Fatal("second Add of one worktree succeeded")
			}

			writeFile(t, filepath.Join(wtRoot, "b.txt"), "b\n")
			git(t, wtRoot, "add", "b.txt")
			h.wait(t, "linked index", is(bus.TypeGitIndexChanged, "linked-1a2b3c4d"))
			git(t, wtRoot, "commit", "-q", "-m", "linked")
			e := h.wait(t, "linked head", is(bus.TypeGitHeadChanged, "linked-1a2b3c4d"))
			if p := payload[bus.GitHeadChanged](t, e); p.Ref != "refs/heads/linked" {
				t.Fatalf("linked payload = %+v", p)
			}
			for _, e := range h.seen {
				if e.Type == bus.TypeGitHeadChanged && e.Worktree == "main" {
					t.Fatalf("main HEAD reported for a linked commit: %s", e.Payload)
				}
			}

			if err := h.w.Remove(testRepo, "linked-1a2b3c4d"); err != nil {
				t.Fatal(err)
			}
			if _, ok := h.w.Mode(testRepo, "linked-1a2b3c4d"); ok {
				t.Fatal("removed worktree still has a mode")
			}
			git(t, root, "worktree", "remove", "--force", wtRoot)
			h.wait(t, "worktree removed", func(e bus.Event) bool { return e.Type == bus.TypeGitWorktreesChanged })
		})
	}
}

func TestConfigEdits(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			root := newRepo(t)
			h := start(t, watch.Options{Mode: mode}, target(t, root, "main"))

			// .agentflow does not exist yet: it is picked up when it appears.
			writeFile(t, filepath.Join(root, ".agentflow", "config.yml"), "version: 1\n")
			e := h.wait(t, "config created", is(bus.TypeConfigLayerChanged, "main"))
			p := payload[bus.ConfigLayerChanged](t, e)
			if p.Layer != bus.LayerRepo || !slices.Equal(p.Paths, []string{".agentflow/config.yml"}) || e.Source != watch.SourceConfig {
				t.Fatalf("config event = %+v %+v", e, p)
			}

			writeFile(t, filepath.Join(root, ".agentflow", "providers", "jira.yaml"), "kind: jira\n")
			e = h.wait(t, "provider created", is(bus.TypeConfigLayerChanged, "main"))
			if p := payload[bus.ConfigLayerChanged](t, e); !slices.Equal(p.Paths, []string{".agentflow/providers/jira.yaml"}) {
				t.Fatalf("provider paths = %v", p.Paths)
			}

			writeFile(t, filepath.Join(root, ".agentflow", "bin", "agentflow"), "#!/bin/sh\n")
			writeFile(t, filepath.Join(root, ".agentflow", "config.yml"), "version: 2\n")
			e = h.wait(t, "config edited", is(bus.TypeConfigLayerChanged, "main"))
			if p := payload[bus.ConfigLayerChanged](t, e); !slices.Equal(p.Paths, []string{".agentflow/config.yml"}) {
				t.Fatalf("edit paths = %v", p.Paths)
			}

			// Rewriting the same content changes nothing.
			writeFile(t, filepath.Join(root, ".agentflow", "config.yml"), "version: 2\n")
			for _, e := range h.quiet(700 * time.Millisecond) {
				if e.Type == bus.TypeConfigLayerChanged {
					t.Fatalf("no-op rewrite reported: %s", e.Payload)
				}
			}
		})
	}
}

func TestEventStoreNeverTriggersEvents(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			root := newRepo(t)
			main := target(t, root, "main")
			h := start(t, watch.Options{Mode: mode}, main)

			events := filepath.Join(main.CommonDir, "agentflow", "events")
			for i := range 20 {
				writeFile(t, filepath.Join(events, "00000000000000000001.jsonl"), strings.Repeat("{}\n", i+1))
				writeFile(t, filepath.Join(events, "owner.lock"), "")
				time.Sleep(10 * time.Millisecond)
			}
			writeFile(t, filepath.Join(main.CommonDir, "objects", "zz", "loose"), "x")
			writeFile(t, filepath.Join(main.CommonDir, "logs", "HEAD"), "x")
			if got := h.quiet(800 * time.Millisecond); len(got) > 0 {
				t.Fatalf("store writes produced events: %v", got)
			}
			writeFile(t, filepath.Join(root, ".agentflow", "config.yml"), "sentinel: true\n")
			e := h.wait(t, "sentinel", func(bus.Event) bool { return true })
			if e.Type != bus.TypeConfigLayerChanged {
				t.Fatalf("first event = %s %s", e.Type, e.Payload)
			}
		})
	}
}

func TestBurstIsDebounced(t *testing.T) {
	for _, mode := range modes {
		t.Run(string(mode), func(t *testing.T) {
			root := newRepo(t)
			writeFile(t, filepath.Join(root, ".agentflow", "config.yml"), "n: 0\n")
			h := start(t, watch.Options{Mode: mode}, target(t, root, "main"))
			for i := range 30 {
				writeFile(t, filepath.Join(root, ".agentflow", "config.yml"), fmt.Sprintf("n: %d\n", i+1))
				time.Sleep(3 * time.Millisecond)
			}
			var n int
			for _, e := range h.quiet(1200 * time.Millisecond) {
				if e.Type == bus.TypeConfigLayerChanged {
					n++
				}
			}
			// 30 writes in ~100 ms: one batch, or two when MaxDelay or a
			// poll tick splits the burst.
			if n < 1 || n > 2 {
				t.Fatalf("burst gave %d config events", n)
			}
		})
	}
}

// failingNotify is a real notify Source that cannot watch one directory.
type failingNotify struct {
	watch.Source
	fail string
}

func (f failingNotify) Add(dir string) error {
	if dir == f.fail {
		return fmt.Errorf("watch %s: %w", dir, watch.ErrUnsupported)
	}
	return f.Source.Add(dir)
}

func TestFallsBackToPollingPerTarget(t *testing.T) {
	root := newRepo(t)
	main := target(t, root, "main")
	refs := filepath.Join(main.CommonDir, "refs")
	h := start(t, watch.Options{NewNotify: func() (watch.Source, error) {
		s, err := watch.NewNotifySource()
		return failingNotify{Source: s, fail: refs}, err
	}}, main)
	if m, ok := h.w.Mode(testRepo, "main"); !ok || m != watch.ModePoll {
		t.Fatalf("mode = %s, %v; want poll", m, ok)
	}
	git(t, root, "branch", "fallback")
	h.wait(t, "polled ref", refsInclude(t, "refs/heads/fallback"))
	git(t, root, "checkout", "-q", "fallback")
	h.wait(t, "checkout", is(bus.TypeGitHeadChanged, "main"))
}

func TestOverflowRescans(t *testing.T) {
	root := newRepo(t)
	fake := watchtest.New(watch.ModeNotify)
	h := start(t, watch.Options{NewNotify: func() (watch.Source, error) { return fake, nil }}, target(t, root, "main"))
	if m, _ := h.w.Mode(testRepo, "main"); m != watch.ModeNotify {
		t.Fatalf("mode = %s", m)
	}
	fake.EmitError(watch.ErrOverflow)
	h.wait(t, "rescan", func(e bus.Event) bool {
		return e.Type == bus.TypeGitRefsChanged && len(payload[bus.GitRefsChanged](t, e).Refs) == 0
	})
	// Nothing else changed, so the rescan reports nothing more.
	for _, e := range h.quiet(400 * time.Millisecond) {
		t.Fatalf("rescan reported %s %s", e.Type, e.Payload)
	}
}

func TestFakeSourceChangesAreRouted(t *testing.T) {
	root := newRepo(t)
	fake := watchtest.New(watch.ModeNotify)
	main := target(t, root, "main")
	h := start(t, watch.Options{NewNotify: func() (watch.Source, error) { return fake, nil }}, main)
	if !fake.Watching(main.GitDir) || !fake.Watching(filepath.Join(main.CommonDir, "refs", "heads")) ||
		fake.Watching(filepath.Join(main.CommonDir, "objects")) {
		t.Fatal("watched directories are wrong")
	}
	git(t, root, "checkout", "-q", "-b", "other")
	fake.Emit(filepath.Join(main.GitDir, "HEAD"), watch.Created)
	e := h.wait(t, "head", is(bus.TypeGitHeadChanged, "main"))
	if p := payload[bus.GitHeadChanged](t, e); p.Ref != "refs/heads/other" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestEnvForcesPolling(t *testing.T) {
	root := newRepo(t)
	t.Setenv(watch.EnvMode, "poll")
	h := start(t, watch.Options{}, target(t, root, "main"))
	if m, _ := h.w.Mode(testRepo, "main"); m != watch.ModePoll {
		t.Fatalf("mode = %s", m)
	}
}

func TestAddRejectsInvalidTargets(t *testing.T) {
	w, err := watch.New(watch.Options{Mode: watch.ModePoll})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	for _, tg := range []watch.Target{
		{},
		{Repo: "bad", Worktree: "main", Root: "/r", GitDir: "/r/.git", CommonDir: "/r/.git"},
		{Repo: testRepo, Root: "/r", GitDir: "/r/.git", CommonDir: "/r/.git"},
	} {
		if err := w.Add(tg); err == nil {
			t.Errorf("Add(%+v) succeeded", tg)
		}
	}
	if err := w.Remove(testRepo, "main"); err == nil {
		t.Error("Remove of an unknown worktree succeeded")
	}
}
