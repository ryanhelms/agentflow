package webui

import (
	"context"
	"strings"
	"testing"
	"time"
)

// waitNavigation waits until the tab id has a pending navigation in the
// hub, as the tabs projection would show it.
func waitNavigation(t *testing.T, h *tabHub, id string) TabNavigation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for _, tab := range h.tabs {
			if tab.id == id && tab.nav != nil {
				nav := *tab.nav
				h.mu.Unlock()
				return nav
			}
		}
		h.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("navigation missing")
	return TabNavigation{}
}

func TestTabNavigationChecksIdentityAndAcknowledgement(t *testing.T) {
	s := testServer(t)
	const id = "deadbeefdeadbeefdeadbeefdeadbeef"
	if w := request(t, s, "POST", "/api/tabs/attach", s.origin, "", map[string]string{"tab": id}); w.Code != 401 || s.tabRoutes().navigateTo(t.Context(), Destination{}, true, "") != tabMissing {
		t.Fatal("anonymous tab registered")
	}
	if w := request(t, s, "GET", "/api/tabs/events?tab="+id, s.origin, "session", nil); w.Code != 404 && w.Code != 405 {
		t.Fatalf("the removed tab stream answered %d", w.Code)
	}
	if !s.tabRoutes().attach(id, s.origin, s.worktreePath, "") {
		t.Fatal("attach failed")
	}
	release := s.tabRoutes().hold([]string{id})
	defer release()
	d := Destination{Screen: "providers", Role: "devops.checks"}
	if s.tabRoutes().navigateTo(t.Context(), d, true, "") != tabAccepted {
		t.Fatal("automatic launch missed the connected tab")
	}
	s.tabRoutes().mu.Lock()
	if nav := s.tabRoutes().tabs[0].nav; nav != nil {
		t.Fatal("automatic launch navigated the tab")
	}
	s.tabRoutes().mu.Unlock()
	for _, expected := range []tabOutcome{tabMissing, tabAccepted, tabStorageFull, tabPublishing, tabAutonomy} {
		result := make(chan tabOutcome, 1)
		go func() { result <- s.tabRoutes().navigateTo(t.Context(), d, false, "") }()
		event := waitNavigation(t, s.tabRoutes(), id)
		if event.Destination != d || event.Tab != id {
			t.Fatal("navigation destination changed")
		}
		body := map[string]any{"id": event.ID, "tab": strings.Repeat("0", 32), "handled": true}
		if w := request(t, s, "POST", "/api/tabs/ack", s.origin, "session", body); w.Code != 404 {
			t.Fatal("accepted another tab's acknowledgement")
		}
		body["tab"], body["handled"] = id, expected == tabAccepted
		if expected.err() != nil {
			body["blocked"] = string(expected)
		}
		if w := request(t, s, "POST", "/api/tabs/ack", s.origin, "session", body); w.Code != 200 {
			t.Fatal("did not accept the tab's acknowledgement")
		}
		if got := <-result; got != expected {
			t.Fatalf("handled = %v, want %v", got, expected)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if s.tabRoutes().navigateTo(ctx, d, false, "") != tabMissing {
		t.Fatal("a cancelled navigation was handled")
	}
}

// Without an event stream (it answers 503) a tab cannot receive navigation,
// so the console refuses to attach it and a navigation request opens a new
// tab at once instead of waiting for an acknowledgement.
func TestTabWithoutEventStreamIsNotAttached(t *testing.T) {
	s := testServer(t) // no event runtime: GET /api/events answers 503
	const id = "0123456789abcdef0123456789abcdef"
	if w := request(t, s, "GET", EventStreamPath, s.origin, "session", nil); w.Code != 503 {
		t.Fatalf("stream = %d", w.Code)
	}
	if w := request(t, s, "POST", "/api/tabs/attach", s.origin, "session", map[string]string{"tab": id}); w.Code != 503 {
		t.Fatalf("attach = %d", w.Code)
	}
	start := time.Now()
	if got := s.tabRoutes().navigateTo(t.Context(), Destination{Screen: "providers"}, false, ""); got != tabMissing {
		t.Fatalf("navigation = %q", got)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("navigation waited %v", d)
	}
}

// A tab that attached but never answers (its stream went away, or it shows a
// page that does not act on navigation) holds a navigation for at most two
// seconds; then the caller opens a new tab.
func TestUnansweredNavigationFallsBack(t *testing.T) {
	s := testServer(t)
	const id = "fedcba9876543210fedcba9876543210"
	if !s.tabRoutes().attach(id, s.origin, s.worktreePath, "") {
		t.Fatal("attach failed")
	}
	start := time.Now()
	if got := s.tabRoutes().navigateTo(t.Context(), Destination{Screen: "providers"}, false, ""); got != tabMissing {
		t.Fatalf("navigation = %q", got)
	}
	if d := time.Since(start); d < 1500*time.Millisecond || d > 3*time.Second {
		t.Fatalf("navigation took %v", d)
	}
}

// A navigation target outside /w/<id>/ (the standalone console at /) is
// never sent to a tab: the caller opens it in a new tab.
func TestNavigationTargetOutsideAWorktreeOpensANewTab(t *testing.T) {
	s := testServer(t)
	const id = "00112233445566778899aabbccddeeff"
	if !s.tabRoutes().attach(id, s.origin, s.worktreePath, "") {
		t.Fatal("attach failed")
	}
	for _, target := range []string{s.origin + "/", s.origin + "/#launch=x", "http://127.0.0.2:1/w/main/"} {
		start := time.Now()
		if got := s.tabRoutes().navigateTo(t.Context(), Destination{}, false, target); got != tabMissing {
			t.Fatalf("%s: navigation = %q", target, got)
		}
		if d := time.Since(start); d > 100*time.Millisecond {
			t.Fatalf("%s: navigation waited %v", target, d)
		}
	}
}
