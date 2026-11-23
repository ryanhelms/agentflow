package webui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func serveCommander(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	// Darwin caps socket paths at 103 bytes, shorter than many t.TempDir paths.
	dir, err := os.MkdirTemp("", "dfcmd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "service.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_COMMANDER_SOCKET", socket)
	t.Setenv("AGENT_COMMANDER_TOKEN", "")
	srv := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(listener) }()
	t.Cleanup(func() {
		_ = srv.Close()
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			t.Error(err)
		}
	})
}

func TestCommanderSocketDiscovery(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		socket, home, want string
	}{
		{"relative.sock", "unused", "relative.sock"},
		{"", "relative-state", filepath.Join("relative-state", "service.sock")},
	} {
		t.Setenv("AGENT_COMMANDER_SOCKET", tc.socket)
		t.Setenv("AGENT_COMMANDER_HOME", tc.home)
		want, err := filepath.Abs(tc.want)
		if err != nil || commanderSocket() != want {
			t.Errorf("socket = %q, want %q (%v)", commanderSocket(), want, err)
		}
	}
}

func TestCommanderReplyScopedConfirmedOnce(t *testing.T) {
	s := testServer(t)
	other := t.TempDir()
	var sent atomic.Int32
	// Keep the list response pending to exercise replay protection even if a
	// refresh returns stale activity. Agent Commander's reply returns Question.
	serveCommander(t, func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Error("the browser session credential was sent to Agent Commander")
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/runs":
			_, _ = fmt.Fprintf(w, `{"runs":[
				{"id":"other","path":%q,"requests":[{"id":"foreign","worker_id":"a","status":"pending","question":"other worktree"}]},
				{"id":"local","title":"Local run","workers":[{"id":"w","name":"Reviewer"}],"requests":[
					{"id":"closed","worker_id":"w","status":"answered","question":"Already done?"},
					{"id":"open","worker_id":"w","status":"pending","question":"Which branch?"}],"path":%q}]}`, other, s.repo.Dir)
		case r.Method == "POST" && r.URL.Path == "/v1/requests/open/reply":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["answer"] != "feature" || len(body) != 1 {
				t.Errorf("unexpected reply body: %#v, %v", body, err)
			}
			sent.Add(1)
			_, _ = io.WriteString(w, `{"id":"open","worker_id":"w","question":"Which branch?","status":"answered","answer":"feature"}`)
		default:
			t.Errorf("unexpected Commander route: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	})
	items, connected, err := commanderActivity(t.Context(), s.repo.Dir)
	if err != nil || !connected || len(items) != 1 || items[0].Request != "open" || items[0].Run != "local" ||
		items[0].Detail != "Which branch?" || !strings.Contains(items[0].Title, "Reviewer") {
		t.Fatalf("activity = %#v, connected = %t: %v", items, connected, err)
	}
	send := func(run, id, answer, approval string, want int) string {
		t.Helper()
		body := map[string]string{"run_id": run, "request_id": id, "answer": answer, "approval": approval}
		w := request(t, s, "POST", "/api/reply", s.origin, "session", body)
		if w.Code != want {
			t.Fatalf("reply to %s/%s: %d, want %d: %s", run, id, w.Code, want, w.Body)
		}
		return w.Body.String()
	}
	send("other", "foreign", "feature", "", 409)
	send("local", "closed", "feature", "", 409)
	var preview struct{ Approval string }
	body := send("local", "open", "feature", "", 200)
	if err := json.Unmarshal([]byte(body), &preview); err != nil || preview.Approval == "" {
		t.Fatalf("missing reply approval: %s", body)
	}
	send("other", "foreign", "feature", preview.Approval, 409)
	send("local", "open", "different", preview.Approval, 409)
	if sent.Load() != 0 {
		t.Fatal("sent a reply before exact confirmation")
	}
	if got := send("local", "open", "feature", preview.Approval, 200); !strings.Contains(got, `"state":"answered"`) {
		t.Fatalf("unexpected delivery receipt: %s", got)
	}
	send("local", "open", "feature", preview.Approval, 409)
	if sent.Load() != 1 {
		t.Errorf("sent %d replies, want one", sent.Load())
	}
}

func TestCommanderHonorsWorkerIdentity(t *testing.T) {
	s := testServer(t)
	var denied atomic.Bool
	serveCommander(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-worker" {
			t.Error("request did not carry the inherited worker credential")
		}
		if denied.Load() || r.Method == "POST" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = fmt.Fprintf(w, `{"runs":[{"id":"run","path":%q,"requests":[{"id":"q","status":"pending","question":"Choose?"}]}]}`, s.repo.Dir)
	})
	t.Setenv("AGENT_COMMANDER_TOKEN", "fixture-worker")
	body := map[string]string{"run_id": "run", "request_id": "q", "answer": "yes"}
	w := request(t, s, "POST", "/api/reply", s.origin, "session", body)
	var review struct{ Approval string }
	if err := json.Unmarshal(w.Body.Bytes(), &review); w.Code != 200 || err != nil || review.Approval == "" {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	body["approval"] = review.Approval
	for range 2 {
		w = request(t, s, "POST", "/api/reply", s.origin, "session", body)
		if w.Code != 403 || !strings.Contains(w.Body.String(), "denied") || strings.Contains(w.Body.String(), "fixture-worker") {
			t.Fatalf("permission denial: %d %s", w.Code, w.Body)
		}
		denied.Store(true)
	}
}

func TestCommanderLongTranscriptHistory(t *testing.T) {
	active, other := t.TempDir(), t.TempDir()
	record := fmt.Sprintf(`{"path":%q,"workers":[{"output":"%s"}]},`, other, strings.Repeat("x", 64<<10))
	serveCommander(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"runs":[`)
		for range 513 {
			if _, err := io.WriteString(w, record); err != nil {
				return
			}
		}
		_, _ = fmt.Fprintf(w, `{"path":%q,"requests":[{"id":"q","question":"After history?","status":"pending"}]}]}`, active)
	})
	items, connected, err := commanderActivity(t.Context(), active)
	if err != nil || !connected || len(items) != 1 || items[0].Detail != "After history?" {
		t.Fatalf("large run list: %#v, %t, %v", items, connected, err)
	}
}
