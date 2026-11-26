package notifysock_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/notifysock"
	"github.com/ryanhelms/agentflow/internal/notifysock/notifysocktest"
)

const repo = bus.RepoID("0123456789abcdef")

func hookEvent() bus.Event {
	return bus.Event{Type: bus.TypeHookRan, Source: "hook:commit-msg", Repo: repo, Worktree: "main", Payload: []byte(`{"hook":"commit-msg","client":"git","outcome":"passed","duration_ms":3}`)}
}

func TestParseEndpoint(t *testing.T) {
	good := []string{"unix:/home/u/.agentflow-ui/abc.notify", "tcp:127.0.0.1:49152"}
	for _, s := range good {
		e, err := notifysock.ParseEndpoint(s)
		if err != nil || e.String() != s {
			t.Errorf("ParseEndpoint(%q) = %v, %v", s, e, err)
		}
	}
	bad := []string{"", "unix:", "unix:relative.sock", "unix:/a/../b", "unix:/" + strings.Repeat("x", 120),
		"tcp:localhost:80", "tcp:0.0.0.0:80", "tcp:10.0.0.1:80", "tcp:[::1]:80", "tcp:127.0.0.1:0", "tcp:127.0.0.1:070000", "tcp:127.0.0.1:080", "http:127.0.0.1:80"}
	for _, s := range bad {
		if _, err := notifysock.ParseEndpoint(s); !errors.Is(err, notifysock.ErrBadEndpoint) {
			t.Errorf("ParseEndpoint(%q) err = %v", s, err)
		}
	}
}

func TestUnixPathFallsBackToTCP(t *testing.T) {
	if p := notifysock.UnixPath("/home/u/.agentflow-ui/abc"); p != "/home/u/.agentflow-ui/abc.notify" {
		t.Fatal(p)
	}
	if p := notifysock.UnixPath("/" + strings.Repeat("x", notifysock.MaxUnixPath)); p != "" {
		t.Fatal("long path kept", p)
	}
}

func TestHandshakeProofs(t *testing.T) {
	key := notifysock.DeriveKey([]byte("instance key"))
	other := notifysock.DeriveKey([]byte("another instance"))
	server := notifysock.ServerProof(key, "cn", "sn")
	client := notifysock.ClientProof(key, "sn", "cn")
	if !notifysock.VerifyProof(server, notifysock.ServerProof(key, "cn", "sn")) {
		t.Fatal("server proof not reproducible")
	}
	if server == client {
		t.Fatal("client could replay the server's proof")
	}
	if notifysock.VerifyProof(server, notifysock.ServerProof(other, "cn", "sn")) {
		t.Fatal("proof verified under another key")
	}
	if notifysock.VerifyProof(server, notifysock.ServerProof(key, "sn", "cn")) {
		t.Fatal("swapped nonces verified")
	}
}

func TestCheck(t *testing.T) {
	ok := hookEvent().Normalize(time.Now())
	if code := notifysock.Check(ok); code != "" {
		t.Fatal(code)
	}
	cases := map[string]func(*bus.Event){
		notifysock.ErrorForbiddenType:   func(e *bus.Event) { e.Type = bus.TypeApprovalResolved },
		notifysock.ErrorForbiddenSource: func(e *bus.Event) { e.Source = "watch.git" },
		notifysock.ErrorInvalidEvent:    func(e *bus.Event) { e.Seq = 9 },
	}
	for want, mutate := range cases {
		e := ok
		mutate(&e)
		if got := notifysock.Check(e); got != want {
			t.Errorf("Check = %q, want %q", got, want)
		}
	}
	for _, src := range []string{"cli", "mcp:claude-code", "hook:pre-bash"} {
		if !notifysock.ValidSource(src) {
			t.Errorf("%s refused", src)
		}
	}
	for _, src := range []string{"daemon", "mcp:", "hook:Pre", "cli:x", "watch.config"} {
		if notifysock.ValidSource(src) {
			t.Errorf("%s accepted", src)
		}
	}
}

func TestDeliverChain(t *testing.T) {
	ctx := context.Background()
	var warnings []error
	warn := func(err error) { warnings = append(warnings, err) }

	// No daemon owns the store: the producer appends itself.
	app := &notifysocktest.Appender{}
	send := &notifysocktest.Sender{}
	if got := notifysock.Deliver(ctx, hookEvent(), app, send, warn); got != notifysock.Stored || len(app.Events()) != 1 || len(send.Events()) != 0 {
		t.Fatal(got, app.Events(), send.Events())
	}

	// The daemon owns the store: the event goes over the socket.
	owned := &notifysocktest.Appender{Err: store.ErrOwned}
	if got := notifysock.Deliver(ctx, hookEvent(), owned, send, warn); got != notifysock.Sent || len(send.Events()) != 1 {
		t.Fatal(got, send.Events())
	}
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}

	// Owned but no socket answers: dropped with one warning.
	gone := &notifysocktest.Sender{Err: notifysock.ErrNoDaemon}
	if got := notifysock.Deliver(ctx, hookEvent(), owned, gone, warn); got != notifysock.Dropped || len(warnings) != 1 || !errors.Is(warnings[0], notifysock.ErrNoDaemon) {
		t.Fatal(got, warnings)
	}

	// Invalid events never reach a store or the socket.
	bad := hookEvent()
	bad.Type = "nope.nope"
	if got := notifysock.Deliver(ctx, bad, app, send, nil); got != notifysock.Dropped || len(app.Events()) != 1 || len(send.Events()) != 1 {
		t.Fatal(got)
	}

	// Without a store and without a locator the event is dropped.
	if got := notifysock.Deliver(ctx, hookEvent(), nil, nil, nil); got != notifysock.Dropped {
		t.Fatal(got)
	}
}

func TestAcceptorPublishesOnlyOwnedRepos(t *testing.T) {
	ctx := context.Background()
	b := bustest.New(bus.Options{})
	defer b.Close()
	a := &notifysocktest.Acceptor{Publisher: b, Owns: func(r bus.RepoID, w bus.WorktreeID) (bool, bool) {
		return r == repo, w == "main"
	}}
	if _, err := a.Accept(ctx, hookEvent().Normalize(time.Now())); err != nil {
		t.Fatal(err)
	}
	other := hookEvent()
	other.Repo = "fedcba9876543210"
	if _, err := a.Accept(ctx, other.Normalize(time.Now())); !errors.Is(err, notifysock.ErrRejected) || !strings.Contains(err.Error(), notifysock.ErrorUnknownRepo) {
		t.Fatal(err)
	}
	wt := hookEvent()
	wt.Worktree = "feature-0badc0de"
	if _, err := a.Accept(ctx, wt.Normalize(time.Now())); !errors.Is(err, notifysock.ErrRejected) || !strings.Contains(err.Error(), notifysock.ErrorUnknownWorktree) {
		t.Fatal(err)
	}
}

func TestOpEventsAreSocketTypes(t *testing.T) {
	for _, typ := range []bus.Type{bus.TypeOpRequested, bus.TypeOpSuggested, bus.TypeOpAwaitingApproval, bus.TypeOpPrepared, bus.TypeOpApplied, bus.TypeOpFailed} {
		if !notifysock.Allowed(typ) {
			t.Errorf("%s is not a socket type", typ)
		}
	}
	for _, typ := range []bus.Type{bus.TypeApprovalRequested, bus.TypeGitHeadChanged, bus.TypeGHPullUpdated} {
		if notifysock.Allowed(typ) {
			t.Errorf("%s must come only from the daemon's own sources", typ)
		}
	}
}
