package webui

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// `ui --serve-child` never hands out a sign-in link: a new console reports
// only that it is ready, and a running one is not even asked. Whoever runs
// it, agent or not, gets nothing to sign in with; the starter asks the
// daemon through the control request, which needs the private registry
// key.
func TestServeChildGivesNoLink(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "registry")
	root := testServer(t).repo.Dir

	// No daemon: the child starts one and its ready callback gets no address.
	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- serveRegisteredAs(ctx, registry, -1, registration(root, "link", "overview"), func(address string, _ bool) { got <- address }, true)
	}()
	select {
	case a := <-got:
		if a != "" {
			t.Errorf("a new console's child reported %q", a)
		}
	case err := <-done:
		t.Fatalf("child: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out")
	}
	// A daemon is running: the child reports it without asking for a link.
	reused := make(chan [2]any, 1)
	if err := serveRegisteredAs(t.Context(), registry, -1, registration(root, "link", "providers"), func(address string, r bool) { reused <- [2]any{address, r} }, true); err != nil {
		t.Fatal(err)
	}
	if r := <-reused; r[0] != "" || r[1] != true {
		t.Errorf("a running console's child reported %v", r)
	}
	// The starter's own route still gets a link.
	if address, found, err := findRegistered(t.Context(), registry, registration(root, "link", "providers"), 0); err != nil || !found || address == "" {
		t.Errorf("findRegistered = %v, %v, %v", address != "", found, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Error(err)
	}
}
