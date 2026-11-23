package webui

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTabNavigationChecksIdentityAndAcknowledgement(t *testing.T) {
	s := testServer(t)
	const id = "deadbeefdeadbeefdeadbeefdeadbeef"
	req := httptest.NewRequest("GET", s.origin+"/api/tabs/events?tab="+id, nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 || s.tabRoutes().navigateTo(t.Context(), Destination{}, true, "") != tabMissing {
		t.Fatal("anonymous tab registered")
	}
	tab := &consoleTab{id: id, origin: s.origin, path: s.worktreePath, events: make(chan navigation), done: make(chan struct{}), pending: map[string]chan tabOutcome{}}
	s.tabRoutes().tabs = []*consoleTab{tab}
	d := Destination{Screen: "providers", Role: "devops.checks"}
	if s.tabRoutes().navigateTo(t.Context(), d, true, "") != tabAccepted {
		t.Fatal("automatic launch missed the connected tab")
	}
	select {
	case <-tab.events:
		t.Fatal("automatic launch navigated the tab")
	default:
	}
	for _, expected := range []tabOutcome{tabMissing, tabAccepted, tabStorageFull, tabPublishing, tabAutonomy} {
		result := make(chan tabOutcome, 1)
		go func() { result <- s.tabRoutes().navigateTo(t.Context(), d, false, "") }()
		var event navigation
		select {
		case event = <-tab.events:
		case <-time.After(2 * time.Second):
			t.Fatal("manual navigation event missing")
		}
		if event.Destination != d {
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
