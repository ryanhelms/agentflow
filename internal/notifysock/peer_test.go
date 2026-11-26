package notifysock

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ryanhelms/agentflow/internal/bus"
)

type nopAcceptor struct{}

func (nopAcceptor) Accept(_ context.Context, e bus.Event) (bus.Event, error) { return e, nil }

// The daemon refuses a Unix peer of another user even when it proves the
// key, and the real peer credentials of this process pass.
func TestPeerUIDCheck(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no peer credentials on " + runtime.GOOS)
	}
	dir, err := os.MkdirTemp("", "ns")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	key := []byte("0123456789abcdef0123456789abcdef")
	l, ep, err := Listen(filepath.Join(dir, "d"))
	if err != nil {
		t.Fatal(err)
	}
	ep.Key = key
	var seen []int
	real := peerUID
	t.Cleanup(func() { peerUID = real })
	foreign := false
	peerUID = func(c net.Conn) (int, bool, error) {
		uid, reported, err := real(c)
		seen = append(seen, uid)
		if !reported {
			return 0, false, errors.New("Unix peer credentials not reported")
		}
		if foreign {
			uid++
		}
		return uid, true, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, l, key, nopAcceptor{}) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	loc := func(context.Context) (Endpoint, error) { return ep, nil }

	c, err := Dial(ctx, loc)
	if err != nil {
		t.Fatal("own user refused:", err)
	}
	_ = c.Close()
	if len(seen) != 1 || seen[0] != os.Geteuid() {
		t.Fatalf("peer uid = %v, want %d", seen, os.Geteuid())
	}
	foreign = true
	_, err = Dial(ctx, loc)
	var r *Rejection
	if !errors.As(err, &r) || r.Code != ErrorAuth {
		t.Fatal("foreign user accepted:", err)
	}
}
