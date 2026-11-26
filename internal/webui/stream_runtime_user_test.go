package webui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/bus/store/storetest"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// userRuntime starts the event runtime with the user-level extension over a
// temporary OS home and agentflow home, and one served repository.
type userRuntime struct {
	rt       *eventRuntime
	repo     *repoEvents
	worktree registeredWorktree
	home     string
	userHome string
	gate     *policy.Gate
	events   bus.Subscription
	global   bus.Subscription

	mu     sync.Mutex
	errors []string
}

// startUserRuntime serves one repository. With a fake global store it
// stands in for the daemon once it opens the real one (openGlobalStore); with
// nil the runtime has no global store, as the daemon has today.
func startUserRuntime(t *testing.T, global *storetest.Store) *userRuntime {
	t.Helper()
	dir := eventRepo(t) // also sets AGENTFLOW_HOME to a temporary directory
	u := &userRuntime{home: os.Getenv("AGENTFLOW_HOME")}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	u.userHome = root
	t.Setenv("HOME", root)
	oldHome := osUserHome
	osUserHome = func() (string, error) { return root, nil }
	t.Cleanup(func() { osUserHome = oldHome })
	oldWatcher := newWatcher
	newWatcher = func() (watch.Watcher, error) {
		return watch.New(watch.Options{
			Translator: watch.NewTranslator(), PollInterval: 50 * time.Millisecond,
			OnError: func(err error) { u.mu.Lock(); u.errors = append(u.errors, err.Error()); u.mu.Unlock() },
		})
	}
	t.Cleanup(func() { newWatcher = oldWatcher })
	if global != nil {
		oldGlobal := openGlobalStore
		openGlobalStore = func(context.Context) (store.Store, error) { return global, nil }
		t.Cleanup(func() { openGlobalStore = oldGlobal })
	}

	ctx := context.Background()
	if u.worktree, _, err = inspectWorktree(ctx, dir); err != nil {
		t.Fatal(err)
	}
	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	u.rt = rt
	if u.repo, err = rt.attach(u.worktree); err != nil {
		t.Fatal(err)
	}
	u.events, err = rt.bus.Subscribe(bus.SubscribeOptions{Name: "test", Buffer: 256}, bus.Topic{Repo: u.repo.id, Worktree: bus.Any, Types: "approval.**"})
	if err != nil {
		t.Fatal(err)
	}
	u.global, err = rt.bus.Subscribe(bus.SubscribeOptions{Name: "test-global", Buffer: 256}, bus.Topic{Repo: bus.GlobalRepo, Worktree: bus.Any, Types: "**"})
	if err != nil {
		t.Fatal(err)
	}
	if u.gate, err = policy.NewGate(u.home, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "claude-code"); err != nil {
		t.Fatal(err)
	}
	u.gate.Configure(u.worktree.Common, nil, nil)
	return u
}

func (u *userRuntime) next(t *testing.T, typ bus.Type, id string) bus.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-u.events.Events():
			var p struct {
				OperationID string `json:"operation_id"`
			}
			if e.Type == typ && json.Unmarshal(e.Payload, &p) == nil && p.OperationID == id {
				return e
			}
		case <-deadline:
			u.mu.Lock()
			defer u.mu.Unlock()
			t.Fatalf("no %s for %s; watcher errors: %v", typ, id, u.errors)
		}
	}
}

func TestUserEventsRouteApprovalsToTheirRepository(t *testing.T) {
	u := startUserRuntime(t, nil)
	if held, err := watch.ConsoleWatches(u.home); err != nil || !held {
		t.Fatalf("the daemon does not mark its home as watched: %v, %v", held, err)
	}

	d, err := u.gate.Check(policy.Remote, "pr.open", "payload", "", false)
	if err != nil || d.Approval == nil {
		t.Fatal(d, err)
	}
	id := d.Approval.OperationID
	e := u.next(t, bus.TypeApprovalRequested, id)
	if e.Seq == 0 || e.Repo != u.repo.id || e.Worktree != "" {
		t.Fatalf("event = %+v; it should be stored in the repository's own store", e)
	}
	if head, _ := u.repo.store.Head(context.Background()); head[u.repo.id] == 0 {
		t.Fatalf("store head = %v", head)
	}
	if _, err := u.gate.Approve(id, d.Token); err != nil {
		t.Fatal(err)
	}
	if r := u.next(t, bus.TypeApprovalResolved, id); !strings.Contains(string(r.Payload), `"state":"approved"`) {
		t.Fatalf("resolved = %s", r.Payload)
	}

	// The approvals projection reads the journal through the source the
	// extension installed.
	src := rtApprovals(u.rt)
	items, err := src.Approvals(context.Background(), u.repo.id)
	if err != nil || len(items) != 1 || items[0].OperationID != id || items[0].State != bus.StateApproved {
		t.Fatalf("approvals source = %+v, %v", items, err)
	}
}

func rtApprovals(rt *eventRuntime) ApprovalSource { return rt.approvalSource() }

// Personal configuration and user client settings belong to the global
// repository. They are published like every other event, and stored when
// the runtime has a global store.
func TestUserEventsPublishGlobalChanges(t *testing.T) {
	global := storetest.New(bus.GlobalRepo)
	u := startUserRuntime(t, global)
	wait := func(typ bus.Type) bus.Event {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case e := <-u.global.Events():
				if e.Type == typ {
					return e
				}
			case <-deadline:
				u.mu.Lock()
				defer u.mu.Unlock()
				t.Fatalf("no %s in the global repository; watcher errors: %v", typ, u.errors)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(u.home, "config.yml"), []byte("autonomy: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e := wait(bus.TypeConfigLayerChanged)
	if e.Seq == 0 || e.Repo != bus.GlobalRepo || e.Worktree != "" || !strings.Contains(string(e.Payload), `"layer":"personal"`) {
		t.Fatalf("personal config event = %+v", e)
	}
	if err := os.MkdirAll(filepath.Join(u.userHome, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(u.userHome, ".claude", "settings.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	e = wait(bus.TypeClientSettingsChanged)
	if e.Seq == 0 || e.Repo != bus.GlobalRepo || !strings.Contains(string(e.Payload), `"scope":"user"`) || !strings.Contains(string(e.Payload), `".claude/settings.json"`) {
		t.Fatalf("client settings event = %+v", e)
	}
	if head, _ := global.Head(context.Background()); head[bus.GlobalRepo] != 2 {
		t.Fatalf("global store head = %v", head)
	}
}

// TODO(global store): the daemon cannot open the durable store for
// bus.GlobalRepo yet (openGlobalStore returns errNoGlobalStore), so the
// events above are published but not kept. When that lane lands this test
// runs on its own: the same two changes must come out of the real store
// with a sequence number, after a reconnect too.
func TestUserEventsAreStoredInTheDurableGlobalStore(t *testing.T) {
	if _, err := openGlobalStore(context.Background()); errors.Is(err, errNoGlobalStore) {
		t.Skip("TODO(global store): the durable global event store does not exist yet")
	}
	u := startUserRuntime(t, nil)
	if err := os.WriteFile(filepath.Join(u.home, "config.yml"), []byte("autonomy: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-u.global.Events():
		if e.Seq == 0 || e.Type != bus.TypeConfigLayerChanged {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no global event from the durable store")
	}
}

func TestUserEventsStopReleasesTheHome(t *testing.T) {
	dir := eventRepo(t)
	home := os.Getenv("AGENTFLOW_HOME")
	oldHome := osUserHome
	osUserHome = func() (string, error) { return filepath.EvalSymlinks(t.TempDir()) }
	t.Cleanup(func() { osUserHome = oldHome })
	rt, stop, err := startEventRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r, _, err := inspectWorktree(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.attach(r); err != nil {
		t.Fatal(err)
	}
	if held, _ := watch.ConsoleWatches(home); !held {
		t.Fatal("home not marked while the runtime runs")
	}
	if c := watch.CheckHome(home, true); !c.Checked || c.Warn {
		t.Fatalf("check while running = %+v", c)
	}
	stop()
	if held, _ := watch.ConsoleWatches(home); held {
		t.Fatal("home still marked after stop")
	}
	if c := watch.CheckHome(home, true); !c.Warn {
		t.Fatalf("check after stop = %+v", c)
	}
}

// A watcher that cannot watch user-level directories leaves the extension
// idle: nothing is started, nothing fails, and attaching still works.
type bareWatcher struct{ watch.Watcher }

func (bareWatcher) Add(watch.Target) error                         { return nil }
func (bareWatcher) Remove(bus.RepoID, bus.WorktreeID) error        { return nil }
func (bareWatcher) Run(ctx context.Context, _ bus.Publisher) error { <-ctx.Done(); return nil }

func TestUserEventsIdleWithoutUserWatching(t *testing.T) {
	dir := eventRepo(t)
	old := newWatcher
	newWatcher = func() (watch.Watcher, error) { return bareWatcher{}, nil }
	t.Cleanup(func() { newWatcher = old })
	rt, stop, err := startEventRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	r, _, err := inspectWorktree(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rt.attach(r); err != nil {
		t.Fatal(err)
	}
	if held, _ := watch.ConsoleWatches(os.Getenv("AGENTFLOW_HOME")); held {
		t.Fatal("an idle extension marked the home as watched")
	}
}

func TestJournalApprovalsListsOneRepository(t *testing.T) {
	u := startUserRuntime(t, nil)
	src := newJournalApprovals(u.rt)
	ctx := context.Background()

	open, err := u.gate.Check(policy.Remote, "pr.open", "open", "", false)
	if err != nil {
		t.Fatal(err)
	}
	done, err := u.gate.Check(policy.Local, "commit", "done", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.gate.Approve(done.Approval.OperationID, done.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := u.gate.Check(policy.Local, "commit", "done", done.Approval.OperationID, false); err != nil {
		t.Fatal(err)
	}
	// Another repository's records, and a past expiry nobody recorded.
	elsewhere, err := policy.NewGate(u.home, config.Autonomy{Local: config.Confirm, Remote: config.Confirm}, "codex")
	if err != nil {
		t.Fatal(err)
	}
	elsewhere.Configure(filepath.Join(u.userHome, "other-repo"), nil, nil)
	if _, err := elsewhere.Check(policy.Remote, "pr.open", "elsewhere", "", false); err != nil {
		t.Fatal(err)
	}
	lapsed := filepath.Join(u.home, "approvals", policy.ScopeKey(u.worktree.Common)+"-op_"+strings.Repeat("0", 31)+"1.json")
	if err := os.WriteFile(lapsed, []byte(`{"operation_id":"op_`+strings.Repeat("0", 31)+`1","state":"pending","action":"push","class":"remote",`+
		`"issued_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-01T00:05:00Z","fingerprint":"SECRET"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := src.Approvals(ctx, u.repo.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
	byID := map[string]ApprovalItem{}
	for _, it := range items {
		byID[it.OperationID] = it
	}
	if it := byID[open.Approval.OperationID]; it.State != bus.StatePending || it.Expired || it.Client != "claude-code" || it.Action != "pr.open" {
		t.Fatalf("open = %+v", it)
	}
	if it := byID[done.Approval.OperationID]; it.State != bus.StateConsumed {
		t.Fatalf("done = %+v", it)
	}
	if it := byID["op_"+strings.Repeat("0", 31)+"1"]; it.State != bus.StatePending || !it.Expired {
		t.Fatalf("lapsed = %+v", it)
	}
	raw, _ := json.Marshal(items)
	if strings.Contains(string(raw), "SECRET") || strings.Contains(string(raw), "fingerprint") {
		t.Fatalf("items leak the record: %s", raw)
	}
	for i := 1; i < len(items); i++ {
		if items[i].IssuedAt.Before(items[i-1].IssuedAt) {
			t.Fatalf("items are not oldest first: %+v", items)
		}
	}
	if none, err := src.Approvals(ctx, "ffffffffffffffff"); err != nil || len(none) != 0 {
		t.Fatalf("unserved repository = %+v, %v", none, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := src.Approvals(cancelled, u.repo.id); err == nil {
		t.Fatal("a cancelled context was ignored")
	}
}
