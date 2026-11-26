package deliver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/notifysock"
	"github.com/ryanhelms/agentflow/internal/notifysock/notifysocktest"
	"github.com/ryanhelms/agentflow/internal/webui"
)

func repo(t *testing.T) (*app.App, webui.WorktreeIdentity) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	id, err := webui.IdentifyWorktree(t.Context(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return &app.App{Repo: &gitx.Repo{Dir: dir}}, id
}

func opEvent(t *testing.T) bus.Event {
	t.Helper()
	return bus.Event{ID: bus.NewID(time.Now()), Type: bus.TypeOpRequested, Source: "cli", CorrelationID: "op_00112233445566778899aabbccddeeff",
		Time: time.Now().UTC(), SchemaVersion: bus.SchemaVersion, Payload: []byte(`{"operation":"eod.post","client":"cli"}`)}
}

func noDaemon(context.Context) (notifysock.Endpoint, error) {
	return notifysock.Endpoint{}, notifysock.ErrNoDaemon
}

func TestNoStoreNoEmitterNoFiles(t *testing.T) {
	a, id := repo(t)
	em, err := Source(Options{Locator: noDaemon})(t.Context(), a)
	if err != nil || em != nil {
		t.Fatalf("emitter %v, err %v for a repository without a store", em, err)
	}
	if _, err := os.Stat(filepath.Join(id.CommonDir, "agentflow")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("store directory exists: %v", err)
	}
	if em, _ := Source(Options{})(t.Context(), nil); em != nil {
		t.Fatal("emitter for no App")
	}
}

func TestAppendsWhenNoDaemonOwnsTheStore(t *testing.T) {
	a, id := repo(t)
	s, err := store.Open(t.Context(), id.CommonDir, store.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	var warn strings.Builder
	em, err := Source(Options{Locator: noDaemon, Warn: &warn})(t.Context(), a)
	if err != nil || em == nil {
		t.Fatalf("no emitter: %v", err)
	}
	if err := em.Emit(t.Context(), opEvent(t), true); err != nil {
		t.Fatal(err)
	}
	r, err := store.Open(t.Context(), id.CommonDir, store.RoleReader)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for ev, err := range r.ReadFrom(t.Context(), bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if ev.Worktree != id.Worktree || ev.Repo != r.Repo() {
			t.Errorf("stored %+v", ev)
		}
	}
	if n != 1 || warn.Len() != 0 {
		t.Fatalf("stored %d events, warned %q", n, warn.String())
	}
}

func TestDropWarnsOncePerProcess(t *testing.T) {
	a, id := repo(t)
	// An owner holds the store and no daemon answers: every event drops.
	s, err := store.Open(t.Context(), id.CommonDir, store.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var warn strings.Builder
	src := Source(Options{Locator: noDaemon, Warn: &warn})
	for range 2 {
		em, _ := src(t.Context(), a)
		if err := em.Emit(t.Context(), opEvent(t), true); err == nil {
			t.Fatal("event delivered without a daemon")
		}
	}
	if got := strings.Count(warn.String(), "\n"); got != 1 || !strings.Contains(warn.String(), "op.requested dropped") {
		t.Fatalf("warnings %q", warn.String())
	}
}

func TestUnknownWorktree(t *testing.T) {
	if !unknownWorktree(&notifysock.Rejection{Code: notifysock.ErrorUnknownWorktree}) ||
		!unknownWorktree(fmt.Errorf("send: %w", &notifysock.Rejection{Code: notifysock.ErrorUnknownWorktree})) ||
		unknownWorktree(&notifysock.Rejection{Code: notifysock.ErrorUnknownRepo}) || unknownWorktree(nil) {
		t.Fatal("unknownWorktree misclassifies")
	}
}

// published records what the fake daemon's Acceptor published.
type published struct {
	mu     sync.Mutex
	events []bus.Event
}

func (p *published) Publish(_ context.Context, e bus.Event) (bus.Event, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
	return e, nil
}

func TestSendsToTheOwningDaemonAndRetriesWithoutUnknownWorktree(t *testing.T) {
	a, id := repo(t)
	s, err := store.Open(t.Context(), id.CommonDir, store.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir, err := os.MkdirTemp("", "ns")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	l, ep, err := notifysock.Listen(filepath.Join(dir, "d"))
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	ep.Key = key
	pub := &published{}
	// The daemon knows the repository but not this worktree yet.
	acc := &notifysocktest.Acceptor{Publisher: pub, Owns: func(r bus.RepoID, _ bus.WorktreeID) (bool, bool) { return r == s.Repo(), false }}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = notifysock.Serve(ctx, l, key, acc) }()
	t.Cleanup(func() { cancel(); <-done })
	var warn strings.Builder
	em, _ := Source(Options{Warn: &warn, Locator: func(context.Context) (notifysock.Endpoint, error) { return ep, nil }})(t.Context(), a)
	if err := em.Emit(t.Context(), opEvent(t), true); err != nil {
		t.Fatalf("emit: %v (warning %q)", err, warn.String())
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.events) != 1 || pub.events[0].Worktree != "" || pub.events[0].Repo != s.Repo() {
		t.Fatalf("published %+v", pub.events)
	}
}
