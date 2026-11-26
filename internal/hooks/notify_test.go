package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/notifysock"
	"github.com/ryanhelms/agentflow/internal/notifysock/notifysocktest"
	"github.com/ryanhelms/agentflow/internal/webui"
)

var realDeliver = deliverEvents

// Hook tests never reach the user's real daemon or write event stores
// unless a test asks for it. They also never inherit the repository boundary
// of an agent session that runs them (AGENTFLOW_SESSION_REPO), which would
// make the hooks refuse work in the tests' temporary repositories; a test
// that needs a boundary sets it on the hook it runs.
func TestMain(m *testing.M) {
	if err := os.Unsetenv("AGENTFLOW_SESSION_REPO"); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	deliverEvents = func(context.Context, string, ...notifyEvent) {}
	notifyLocator = func(context.Context) (notifysock.Endpoint, error) {
		return notifysock.Endpoint{}, notifysock.ErrNoDaemon
	}
	os.Exit(m.Run())
}

type recorder struct {
	mu     sync.Mutex
	events []notifyEvent
}

func record(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	old := deliverEvents
	deliverEvents = func(_ context.Context, _ string, events ...notifyEvent) {
		r.mu.Lock()
		r.events = append(r.events, events...)
		r.mu.Unlock()
	}
	t.Cleanup(func() { deliverEvents = old })
	return r
}

func (r *recorder) hookRuns(t *testing.T) []bus.HookRan {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []bus.HookRan
	for _, e := range r.events {
		if e.Type == bus.TypeHookRan {
			out = append(out, e.Payload.(bus.HookRan))
		}
	}
	return out
}

func gitRepo(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	run(t, dir, "init", "-q", "-b", "main")
	return dir
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=test@example.org"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

// pre-bash runs before every shell command: it publishes only when it
// blocks or fails, with a fixed reason code and never the command.
func TestPreBashPublishesOnlyBlocked(t *testing.T) {
	dir := gitRepo(t)
	t.Chdir(dir)
	rec := record(t)
	ctx := context.Background()
	in := func(cmd string) *strings.Reader {
		raw, _ := json.Marshal(map[string]any{"tool_input": map[string]string{"command": cmd}})
		return strings.NewReader(string(raw))
	}
	if err := PreBash(ctx, in("ls -la"), &bytes.Buffer{}, true); err != nil {
		t.Fatal(err)
	}
	if got := rec.hookRuns(t); len(got) != 0 {
		t.Fatalf("allowed command published %+v", got)
	}
	if err := PreBash(ctx, in(`git commit -m "updated stuff"`), &bytes.Buffer{}, true); err == nil {
		t.Fatal("invalid message allowed")
	}
	got := rec.hookRuns(t)
	if len(got) != 1 || got[0].Hook != "pre-bash" || got[0].Outcome != bus.HookBlocked || got[0].Reason != ReasonConvention || got[0].Client != "claude" {
		t.Fatalf("hook.ran = %+v", got)
	}
	raw, _ := json.Marshal(rec.events[0].Payload)
	if strings.Contains(string(raw), "updated stuff") || strings.Contains(string(raw), "git commit") {
		t.Fatalf("payload leaks the command: %s", raw)
	}
}

func TestHooksPublishOutcome(t *testing.T) {
	dir := gitRepo(t)
	t.Chdir(dir)
	rec := record(t)
	ctx := context.Background()
	if err := Stop(ctx, "codex", strings.NewReader("{}"), &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	push := strings.NewReader("refs/heads/main abc refs/heads/main def\n")
	if err := PrePush(ctx, push, &bytes.Buffer{}); err == nil {
		t.Fatal("push to main allowed")
	}
	got := rec.hookRuns(t)
	if len(got) != 2 || got[0].Hook != "stop" || got[0].Client != "codex" || got[0].Outcome != bus.HookPassed ||
		got[1].Hook != "pre-push" || got[1].Outcome != bus.HookBlocked || got[1].Reason != ReasonProtectedBranch {
		t.Fatalf("hook.ran = %+v", got)
	}
}

// CommitMsg publishes only as `agentflow hook commit-msg`, not as
// `agentflow lint-msg`, which pre-push and CI run for every commit.
func TestCommitMsgPublishesOnlyAsHook(t *testing.T) {
	dir := gitRepo(t)
	t.Chdir(dir)
	rec := record(t)
	f := filepath.Join(dir, "MSG")
	// lint-msg (CommitMsg) validates every commit of a push or a PR and is not a hook run.
	_ = os.WriteFile(f, []byte("feat: add x\n"), 0o644)
	if err := CommitMsg(context.Background(), f, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(f, []byte("updated stuff\n"), 0o644)
	var ee *ExitError
	if err := CommitMsg(context.Background(), f, &bytes.Buffer{}); !errors.As(err, &ee) {
		t.Fatal(err)
	}
	if got := rec.hookRuns(t); len(got) != 0 {
		t.Fatalf("lint-msg published %+v", got)
	}
	// The git hook publishes, whatever the process's arguments are.
	if err := CommitMsgHook(context.Background(), f, &bytes.Buffer{}); !errors.As(err, &ee) {
		t.Fatal(err)
	}
	got := rec.hookRuns(t)
	if len(got) != 1 || got[0].Hook != "commit-msg" || got[0].Client != "git" || got[0].Outcome != bus.HookBlocked || got[0].Reason != ReasonConvention {
		t.Fatalf("hook.ran = %+v", got)
	}
	_ = os.WriteFile(f, []byte("feat: add x\n"), 0o644)
	if err := CommitMsgHook(context.Background(), f, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if got := rec.hookRuns(t); len(got) != 2 || got[1].Outcome != bus.HookPassed {
		t.Fatalf("hook.ran after a valid message = %+v", got)
	}
}

func TestPostCommitPublishesCommitCreated(t *testing.T) {
	dir := gitRepo(t)
	t.Chdir(dir)
	rec := record(t)
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "feat(PLAT-12)!: add quota API\n\nsecret body text")
	if err := PostCommit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(rec.events) != 2 || rec.events[0].Type != bus.TypeCommitCreated || rec.events[1].Type != bus.TypeHookRan {
		t.Fatalf("events = %+v", rec.events)
	}
	c := rec.events[0].Payload.(bus.CommitCreated)
	if len(c.OID) != 40 || c.Branch != "main" || c.Subject != "feat(PLAT-12)!: add quota API" || c.Type != "feat" || !c.Breaking ||
		len(c.Keys) != 1 || c.Keys[0] != "PLAT-12" {
		t.Fatalf("commit.created = %+v", c)
	}
	raw, _ := json.Marshal(c)
	if strings.Contains(string(raw), "secret body") {
		t.Fatal("payload carries the body")
	}
	// Outside a repository the hook still succeeds silently.
	t.Chdir(t.TempDir())
	if err := PostCommit(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A hook never creates an event store in a repository that has none.
func TestDeliverNeverCreatesStore(t *testing.T) {
	dir := gitRepo(t)
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "chore: init")
	realDeliver(context.Background(), dir, hookRanEvent("stop", "claude", time.Now(), bus.HookPassed, ""))
	id, err := webui.IdentifyWorktree(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(id.CommonDir, "agentflow")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hook created an event store:", err)
	}
}

// With an existing store and no daemon the hook appends to it itself.
func TestDeliverWithoutDaemonStores(t *testing.T) {
	dir := gitRepo(t)
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "chore: init")
	createStore(t, dir)
	realDeliver(context.Background(), dir, hookRanEvent("stop", "claude", time.Now(), bus.HookPassed, ""))
	events := stored(t, dir)
	if len(events) != 1 || events[0].Type != bus.TypeHookRan || events[0].Source != "hook:stop" || events[0].Worktree != "main" {
		t.Fatalf("stored %+v", events)
	}
}

// With a daemon owning the store the hook sends over the socket, inside its
// budget: it does not wait out the store's lock retries.
func TestDeliverToOwningDaemon(t *testing.T) {
	ctx := context.Background()
	dir := gitRepo(t)
	run(t, dir, "commit", "-q", "--allow-empty", "-m", "chore: init")
	id, err := webui.IdentifyWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := store.Open(ctx, id.CommonDir, store.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	b := bustest.New(bus.Options{Interceptors: []bus.Interceptor{store.Interceptor(func(r bus.RepoID) (store.Store, bool) {
		return owner, r == owner.Repo()
	})}})
	defer b.Close()
	sockDir, err := os.MkdirTemp("", "hk")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(sockDir)
	l, ep, err := notifysock.Listen(filepath.Join(sockDir, "d"))
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	ep.Key = key
	sctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = notifysock.Serve(sctx, l, key, &notifysocktest.Acceptor{Publisher: b})
	}()
	defer func() { cancel(); <-done }()
	old := notifyLocator
	notifyLocator = func(context.Context) (notifysock.Endpoint, error) { return ep, nil }
	t.Cleanup(func() { notifyLocator = old })

	start := time.Now()
	realDeliver(ctx, dir, hookRanEvent("pre-bash", "claude", start, bus.HookBlocked, ReasonNeverList))
	if took := time.Since(start); took > NotifyBudget {
		t.Errorf("delivery took %v", took)
	}
	var events []bus.Event
	for e, err := range owner.ReadFrom(ctx, bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if len(events) != 1 || events[0].Source != "hook:pre-bash" {
		t.Fatalf("owner stored %+v", events)
	}
}

// createStore creates dir's event store, as the daemon does on first use.
func createStore(t *testing.T, dir string) {
	t.Helper()
	id, err := webui.IdentifyWorktree(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(context.Background(), id.CommonDir, store.RoleAppender)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func stored(t *testing.T, dir string) []bus.Event {
	t.Helper()
	ctx := context.Background()
	id, err := webui.IdentifyWorktree(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(ctx, id.CommonDir, store.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var out []bus.Event
	for e, err := range s.ReadFrom(ctx, bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

func TestTruncateKeepsRunes(t *testing.T) {
	if got := truncate("héllo", 2); got != "h" {
		t.Fatalf("%q", got)
	}
	if got := truncate("abc", 5); got != "abc" {
		t.Fatal(got)
	}
}
