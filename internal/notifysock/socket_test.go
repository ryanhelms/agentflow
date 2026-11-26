package notifysock_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/bus/bustest"
	"github.com/ryanhelms/agentflow/internal/bus/store"
	"github.com/ryanhelms/agentflow/internal/notifysock"
	"github.com/ryanhelms/agentflow/internal/notifysock/notifysocktest"
)

var instanceKey = []byte("0123456789abcdef0123456789abcdef")

// shortPrefix is a registry prefix short enough for a Unix socket.
func shortPrefix(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "ns")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "d")
}

type daemon struct {
	ep     notifysock.Endpoint
	bus    *bustest.Bus
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func (d *daemon) locator() notifysock.Locator {
	return func(context.Context) (notifysock.Endpoint, error) { return d.ep, nil }
}

func (d *daemon) stop(t *testing.T) {
	t.Helper()
	d.cancel()
	<-d.done
	if d.err != nil {
		t.Fatal(d.err)
	}
}

// startDaemon serves an Acceptor publishing to a test bus for repo.
func startDaemon(t *testing.T, prefix string, a notifysock.Acceptor) *daemon {
	t.Helper()
	l, ep, err := notifysock.Listen(prefix)
	if err != nil {
		t.Fatal(err)
	}
	ep.Key = instanceKey
	ctx, cancel := context.WithCancel(context.Background())
	d := &daemon{ep: ep, cancel: cancel, done: make(chan struct{})}
	if a == nil {
		d.bus = bustest.New(bus.Options{})
		t.Cleanup(func() { _ = d.bus.Close() })
		a = &notifysocktest.Acceptor{Publisher: d.bus, Owns: func(r bus.RepoID, w bus.WorktreeID) (bool, bool) {
			return r == repo, w == "main"
		}}
	}
	go func() {
		defer close(d.done)
		d.err = notifysock.Serve(ctx, l, instanceKey, a)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-d.done:
		case <-time.After(5 * time.Second):
		}
	})
	return d
}

func normalized() bus.Event { return hookEvent().Normalize(time.Now()) }

func TestSendOverUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses loopback TCP")
	}
	prefix := shortPrefix(t)
	d := startDaemon(t, prefix, nil)
	if d.ep.Network != "unix" || d.ep.Address != prefix+".notify" {
		t.Fatalf("endpoint = %v", d.ep)
	}
	info, err := os.Lstat(d.ep.Address)
	if err != nil || info.Mode().Perm() != 0o600 || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("socket mode = %v, %v", info.Mode(), err)
	}
	if _, err := notifysock.ParseEndpoint(d.ep.String()); err != nil {
		t.Fatal(err)
	}
	sendAndSee(t, d)
}

func TestSendOverLoopbackTCP(t *testing.T) {
	// A prefix too long for sun_path falls back to 127.0.0.1.
	prefix := "/" + strings.Repeat("x", notifysock.MaxUnixPath)
	d := startDaemon(t, prefix, nil)
	if d.ep.Network != "tcp" || !strings.HasPrefix(d.ep.Address, "127.0.0.1:") {
		t.Fatalf("endpoint = %v", d.ep)
	}
	if _, err := notifysock.ParseEndpoint(d.ep.String()); err != nil {
		t.Fatal(err)
	}
	sendAndSee(t, d)
}

func sendAndSee(t *testing.T, d *daemon) {
	t.Helper()
	ctx := context.Background()
	sub, err := d.bus.Subscribe(bus.SubscribeOptions{Name: "test"}, bus.Topic{Repo: repo, Worktree: bus.Any, Types: "**"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := notifysock.Dial(ctx, d.locator())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Send(ctx, normalized()); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-sub.Events():
		if e.Type != bus.TypeHookRan || e.Source != "hook:commit-msg" {
			t.Fatalf("published %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("event not published")
	}
}

func TestSendRefusals(t *testing.T) {
	ctx := context.Background()
	d := startDaemon(t, shortPrefix(t), nil)
	c, err := notifysock.Dial(ctx, d.locator())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cases := map[string]func(*bus.Event){
		notifysock.ErrorForbiddenType:   func(e *bus.Event) { e.Type = bus.TypeApprovalResolved },
		notifysock.ErrorForbiddenSource: func(e *bus.Event) { e.Source = "watch.git" },
		notifysock.ErrorInvalidEvent:    func(e *bus.Event) { e.Seq = 4 },
		notifysock.ErrorUnknownRepo:     func(e *bus.Event) { e.Repo = "fedcba9876543210" },
		notifysock.ErrorUnknownWorktree: func(e *bus.Event) { e.Worktree = "feature-0badc0de" },
	}
	for code, mutate := range cases {
		e := normalized()
		mutate(&e)
		err := c.Send(ctx, e)
		var r *notifysock.Rejection
		if !errors.Is(err, notifysock.ErrRejected) || !errors.As(err, &r) || r.Code != code {
			t.Errorf("%s: Send = %v", code, err)
		}
	}
	// A refusal keeps the connection usable.
	if err := c.Send(ctx, normalized()); err != nil {
		t.Fatal(err)
	}
}

func TestNoDaemon(t *testing.T) {
	ctx := context.Background()
	prefix := shortPrefix(t)
	path := prefix + ".notify"
	// No locator entry.
	none := func(context.Context) (notifysock.Endpoint, error) {
		return notifysock.Endpoint{}, notifysock.ErrNoDaemon
	}
	if _, err := notifysock.Dial(ctx, none); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal(err)
	}
	// A registry entry but nothing listening.
	gone := func(context.Context) (notifysock.Endpoint, error) {
		return notifysock.Endpoint{Network: "unix", Address: path, Key: instanceKey}, nil
	}
	if _, err := notifysock.Dial(ctx, gone); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	// A stale socket file left by a crashed daemon: nobody accepts, and
	// the next daemon's Listen replaces it.
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = l.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("stale socket not left behind", err)
	}
	if _, err := notifysock.Dial(ctx, gone); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal(err)
	}
	d := startDaemon(t, prefix, nil)
	if c, err := notifysock.Dial(ctx, d.locator()); err != nil {
		t.Fatal("daemon did not replace the stale socket:", err)
	} else {
		_ = c.Close()
	}
	// Listen never removes something that is not a socket.
	other := shortPrefix(t)
	if err := os.WriteFile(other+".notify", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := notifysock.Listen(other); err == nil {
		t.Fatal("Listen replaced a regular file")
	}
}

// A listener that does not know the instance key (a foreign or recycled
// listener) learns only the client's nonce, and the client reports no daemon.
func TestForeignListenerIsNoDaemon(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	received := make(chan []string, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		first, _ := r.ReadString('\n')
		var hello notifysock.Hello
		_ = json.Unmarshal([]byte(first), &hello)
		other := notifysock.DeriveKey([]byte("another daemon"))
		_ = json.NewEncoder(c).Encode(notifysock.Challenge{V: 1, Nonce: hello.Nonce, Proof: notifysock.ServerProof(other, hello.Nonce, hello.Nonce)})
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		rest, _ := r.ReadString('\n')
		received <- []string{first, rest}
	}()
	loc := func(context.Context) (notifysock.Endpoint, error) {
		return notifysock.Endpoint{Network: "tcp", Address: l.Addr().String(), Key: instanceKey}, nil
	}
	if _, err := notifysock.Dial(context.Background(), loc); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal(err)
	}
	got := <-received
	if !strings.Contains(got[0], `"nonce"`) || got[1] != "" {
		t.Fatalf("foreign listener received %q", got)
	}
}

// A client with the wrong key fails at the daemon's proof; a raw client
// that skips the key is refused with auth.
func TestWrongKeyRefused(t *testing.T) {
	ctx := context.Background()
	d := startDaemon(t, shortPrefix(t), nil)
	wrong := func(context.Context) (notifysock.Endpoint, error) {
		ep := d.ep
		ep.Key = []byte("not the instance key")
		return ep, nil
	}
	if _, err := notifysock.Dial(ctx, wrong); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal(err)
	}
	c, r := rawConn(t, d.ep)
	writeLine(t, c, notifysock.Hello{V: 1, Nonce: nonce()})
	var ch notifysock.Challenge
	readLine(t, r, &ch)
	writeLine(t, c, notifysock.Proof{Proof: notifysock.ClientProof(notifysock.DeriveKey([]byte("guess")), ch.Nonce, "x")})
	var ready notifysock.Ready
	readLine(t, r, &ready)
	if ready.OK || ready.Error == nil || ready.Error.Code != notifysock.ErrorAuth {
		t.Fatalf("ready = %+v", ready)
	}
	// Publishing without the handshake is refused and closes the connection.
	c2, r2 := rawConn(t, d.ep)
	writeLine(t, c2, notifysock.Request{ID: 1, Op: notifysock.OpPublish, Event: ptr(normalized())})
	var refused notifysock.Challenge
	readLine(t, r2, &refused)
	if refused.Error == nil || refused.Error.Code != notifysock.ErrorVersion {
		t.Fatalf("request before the handshake: %+v", refused)
	}
	if _, err := r2.ReadString('\n'); err == nil {
		t.Fatal("connection stayed open")
	}
	writeOnly := func(v int) notifysock.Challenge {
		c, r := rawConn(t, d.ep)
		writeLine(t, c, notifysock.Hello{V: v, Nonce: nonce()})
		var ch notifysock.Challenge
		readLine(t, r, &ch)
		return ch
	}
	if ch := writeOnly(2); ch.Error == nil || ch.Error.Code != notifysock.ErrorVersion {
		t.Fatalf("version: %+v", ch)
	}
}

func TestFrameLimit(t *testing.T) {
	d := startDaemon(t, shortPrefix(t), nil)
	c, r := handshaken(t, d.ep)
	big := make([]byte, notifysock.MaxFrame+10)
	for i := range big {
		big[i] = 'x'
	}
	big[len(big)-1] = '\n'
	go func() { _, _ = c.Write(big) }()
	var resp notifysock.Response
	readLine(t, r, &resp)
	if resp.Error == nil || resp.Error.Code != notifysock.ErrorFrame {
		t.Fatalf("oversized frame: %+v", resp)
	}
	if _, err := r.ReadString('\n'); err == nil {
		t.Fatal("connection stayed open after a frame error")
	}
	c, r = handshaken(t, d.ep)
	if _, err := c.Write([]byte("{not json\n")); err != nil {
		t.Fatal(err)
	}
	readLine(t, r, &resp)
	if resp.Error == nil || resp.Error.Code != notifysock.ErrorFrame {
		t.Fatalf("malformed frame: %+v", resp)
	}
}

func TestConnectionLimit(t *testing.T) {
	ctx := context.Background()
	d := startDaemon(t, shortPrefix(t), nil)
	var clients []*notifysock.Client
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()
	for range notifysock.MaxConnections {
		c, err := notifysock.Dial(ctx, d.locator())
		if err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
	}
	_, err := notifysock.Dial(ctx, d.locator())
	var r *notifysock.Rejection
	if !errors.As(err, &r) || r.Code != notifysock.ErrorBusy {
		t.Fatalf("connection %d: %v", notifysock.MaxConnections+1, err)
	}
	_ = clients[0].Close()
	clients = clients[1:]
	deadline := time.Now().Add(2 * time.Second)
	for {
		c, err := notifysock.Dial(ctx, d.locator())
		if err == nil {
			clients = append(clients, c)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot not released:", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// gate is an Acceptor that holds every event until released and records
// how many Accept calls ran at once.
type gate struct {
	release chan struct{}
	mu      sync.Mutex
	active  int
	max     int
	ids     []string
	pub     bus.Publisher
}

func (g *gate) Accept(ctx context.Context, e bus.Event) (bus.Event, error) {
	g.mu.Lock()
	g.active++
	g.max = max(g.max, g.active)
	g.ids = append(g.ids, e.ID)
	g.mu.Unlock()
	<-g.release
	g.mu.Lock()
	g.active--
	g.mu.Unlock()
	return g.pub.Publish(ctx, e)
}

// A fast producer waits for the daemon instead of losing events: the
// daemon handles one request per connection at a time, in order, and the
// client caps what it has in flight.
func TestBackPressure(t *testing.T) {
	b := bustest.New(bus.Options{})
	defer b.Close()
	g := &gate{release: make(chan struct{}), pub: b}
	d := startDaemon(t, shortPrefix(t), g)
	c, err := notifysock.Dial(context.Background(), d.locator())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	const n = 3 * notifysock.MaxInFlight
	var wg sync.WaitGroup
	var failed atomic.Int32
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c.Send(ctx, normalized()); err != nil {
				failed.Add(1)
				t.Error(err)
			}
		}()
	}
	time.Sleep(50 * time.Millisecond)
	g.mu.Lock()
	waiting := len(g.ids)
	g.mu.Unlock()
	if waiting != 1 {
		t.Fatalf("%d requests reached the acceptor while the first was held", waiting)
	}
	close(g.release)
	wg.Wait()
	if failed.Load() != 0 || len(g.ids) != n || g.max != 1 {
		t.Fatalf("failed %d, accepted %d, concurrency %d", failed.Load(), len(g.ids), g.max)
	}
}

// When the daemon restarts at the same endpoint, the next Send dials again.
func TestClientRedials(t *testing.T) {
	ctx := context.Background()
	prefix := shortPrefix(t)
	d := startDaemon(t, prefix, nil)
	c, err := notifysock.Dial(ctx, d.locator())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Send(ctx, normalized()); err != nil {
		t.Fatal(err)
	}
	d.stop(t)
	if err := c.Send(ctx, normalized()); !errors.Is(err, notifysock.ErrNoDaemon) {
		t.Fatal("send to a stopped daemon:", err)
	}
	d2 := startDaemon(t, prefix, nil)
	if d2.ep.String() != d.ep.String() {
		t.Skip("endpoint changed across restarts (TCP)")
	}
	if err := c.Send(ctx, normalized()); err != nil {
		t.Fatal("send after restart:", err)
	}
}

// When the daemon owns the store, Deliver hands the event over the socket
// and the daemon appends it as owner. This replaces the phase 1 rule
// "drop on store.ErrOwned".
func TestDeliverSendsOverSocketWhenOwned(t *testing.T) {
	ctx := context.Background()
	common := t.TempDir()
	owner, err := store.Open(ctx, common, store.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	appender, err := store.Open(ctx, common, store.RoleAppender)
	if err != nil {
		t.Fatal(err)
	}
	defer appender.Close()
	b := bustest.New(bus.Options{Interceptors: []bus.Interceptor{store.Interceptor(func(id bus.RepoID) (store.Store, bool) {
		return owner, id == owner.Repo()
	})}})
	defer b.Close()
	d := startDaemon(t, shortPrefix(t), &notifysocktest.Acceptor{Publisher: b, Owns: func(r bus.RepoID, _ bus.WorktreeID) (bool, bool) {
		return r == owner.Repo(), true
	}})
	c, err := notifysock.Dial(ctx, d.locator())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	e := hookEvent()
	e.Repo = owner.Repo()
	var warned error
	if got := notifysock.Deliver(ctx, e, appender, c, func(err error) { warned = err }); got != notifysock.Sent || warned != nil {
		t.Fatal(got, warned)
	}
	var stored []bus.Event
	for ev, err := range owner.ReadFrom(ctx, bus.Cursor{}) {
		if err != nil {
			t.Fatal(err)
		}
		stored = append(stored, ev)
	}
	if len(stored) != 1 || stored[0].Type != bus.TypeHookRan || stored[0].Seq != 1 {
		t.Fatalf("stored %+v", stored)
	}
	// The daemon gone: Deliver drops with one warning.
	d.stop(t)
	if got := notifysock.Deliver(ctx, e, appender, c, func(err error) { warned = err }); got != notifysock.Dropped || !errors.Is(warned, notifysock.ErrNoDaemon) {
		t.Fatal(got, warned)
	}
}

func ptr[T any](v T) *T { return &v }

func nonce() string { return strings.Repeat("A", 43) }

func rawConn(t *testing.T, ep notifysock.Endpoint) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, err := net.Dial(ep.Network, ep.Address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	return c, bufio.NewReader(c)
}

func handshaken(t *testing.T, ep notifysock.Endpoint) (net.Conn, *bufio.Reader) {
	t.Helper()
	c, r := rawConn(t, ep)
	key := notifysock.DeriveKey(instanceKey)
	cn := nonce()
	writeLine(t, c, notifysock.Hello{V: 1, Nonce: cn})
	var ch notifysock.Challenge
	readLine(t, r, &ch)
	if !notifysock.VerifyProof(notifysock.ServerProof(key, cn, ch.Nonce), ch.Proof) {
		t.Fatal("daemon proof invalid")
	}
	writeLine(t, c, notifysock.Proof{Proof: notifysock.ClientProof(key, ch.Nonce, cn)})
	var ready notifysock.Ready
	readLine(t, r, &ready)
	if !ready.OK {
		t.Fatalf("ready = %+v", ready)
	}
	return c, r
}

func writeLine(t *testing.T, c net.Conn, v any) {
	t.Helper()
	if err := json.NewEncoder(c).Encode(v); err != nil {
		t.Fatal(err)
	}
}

func readLine(t *testing.T, r *bufio.Reader, v any) {
	t.Helper()
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(line), v); err != nil {
		t.Fatal(err)
	}
}
