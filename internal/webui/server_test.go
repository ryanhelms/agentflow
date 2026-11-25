package webui

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nscaledev/agentflow/internal/consolepublish"
	"github.com/nscaledev/agentflow/internal/gitx"
)

func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }

func testServer(t *testing.T) *server {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", t.TempDir())
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"remote", "add", "origin", "https://github.com/example/agentflow-console"}, {"-c", "user.name=Fixture", "-c", "user.email=test@example.org", "commit", "-q", "--allow-empty", "-m", "chore: initial commit"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agentflow/config.yml"), []byte("agentflow: 1\ncommit:\n  require_key: false\ntask_management:\n  provider: github-issues\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return &server{repo: &gitx.Repo{Dir: dir}, origin: "http://127.0.0.1:41511", instanceKey: randomKey(), session: randomKey(), rememberKey: randomKey()}
}

func request(t *testing.T, s *server, method, path, origin, key string, body any, commander ...string) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(method, s.origin+path, bytes.NewReader(raw))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if key == "session" {
		key = grantToken(s.session, "tab/v1", time.Now().Add(time.Hour))
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	if len(commander) == 1 {
		req.Header.Set("X-Agentflow-Commander", commander[0])
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Agentflow-Request", "console")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

func TestConsoleSessionBoundary(t *testing.T) {
	s := testServer(t)
	launch := mintLaunch(s.instanceKey, time.Now())
	initial := request(t, s, "GET", "/", "", "", nil)
	if initial.Code != 200 || !strings.Contains(initial.Body.String(), "<!") {
		t.Fatal(initial.Code)
	}
	for _, tc := range []struct {
		path, method, origin, key string
		body                      any
		want                      int
	}{
		{"/api/state", "GET", "", "", nil, 401},
		{"/api/session", "POST", "https://example.invalid", "", map[string]string{"launch": launch}, 403},
		{"/api/session", "POST", s.origin, "", map[string]string{"launch": launch}, 200},
		{"/api/session", "POST", s.origin, "", map[string]string{"launch": launch}, 401},
		{"/api/state", "GET", "", "session", nil, 200},
		{"/api/preview", "POST", "", "session", nil, 403},
	} {
		w := request(t, s, tc.method, tc.path, tc.origin, tc.key, tc.body)
		if w.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "http://anything.example/api/state", nil)
	req.Header.Set("Authorization", "Bearer session")
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("wrong host: %v", w.Code)
	}
}

func TestPreviewBoundToExactEdit(t *testing.T) {
	s := testServer(t)
	file := filepath.Join(s.repo.Dir, ".agentflow/config.yml")
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	in := edit{Path: ".agentflow/config.yml", Base: hash(original), Content: string(original) + "  settings:\n    search: {limit: 48}\n", Title: "chore: tune search limit", Draft: true}
	w := request(t, s, "POST", "/api/preview", s.origin, "session", in)
	if w.Code != 200 {
		t.Fatalf("preview %d: %s", w.Code, w.Body.String())
	}
	var p struct {
		Approval string
		Changes  []struct{ Path string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil || p.Approval == "" || len(p.Changes) == 0 {
		t.Fatalf("invalid preview: %s", w.Body.String())
	}
	in.Approval = p.Approval
	in.Title = "chore: change reviewed intent"
	w = request(t, s, "POST", "/api/publish", s.origin, "session", in)
	if w.Code != 409 {
		t.Fatalf("changed intent: %d", w.Code)
	}
	in.Base = "old"
	w = request(t, s, "POST", "/api/preview", s.origin, "session", in)
	if w.Code != 409 {
		t.Fatalf("stale base: %d", w.Code)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatal("preview modified local file")
	}
}

func TestSuggestOnlyPreventsPublishing(t *testing.T) {
	s := testServer(t)
	home := os.Getenv("AGENTFLOW_HOME")
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte("autonomy: {remote: suggest}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(s.repo.Dir, ".agentflow", "config.yml"))
	if err != nil {
		t.Fatal(err)
	}
	in := edit{Path: ".agentflow/config.yml", Base: hash(original), Content: string(original) + "# reviewed\n", Title: "chore: document configuration"}
	w := request(t, s, "POST", "/api/publish", s.origin, "session", in)
	if w.Code != 403 {
		t.Fatalf("suggest only: %d %s", w.Code, w.Body.String())
	}
}

func TestConfirmedPreviewPublishesOnlyOnce(t *testing.T) {
	s := testServer(t)
	const path = ".agentflow/config.yml"
	original, err := os.ReadFile(filepath.Join(s.repo.Dir, path))
	if err != nil {
		t.Fatal(err)
	}
	in := edit{Path: path, Base: hash(original), Content: string(original) + "# shared\n", Title: "chore: document configuration", Draft: true}
	calls := 0
	s.publishTo = func(_ context.Context, root, destination, file, before, after, title string, draft bool, _ map[string]string, task ...string) (consolepublish.Result, error) {
		calls++
		if root != s.repo.Dir || destination != "example/agentflow-console" || file != path || before != string(original) || after != in.Content || title != in.Title || !draft {
			t.Fatal("published arguments differ from review")
		}
		return consolepublish.Result{State: "draft"}, nil
	}
	w := request(t, s, "POST", "/api/preview", s.origin, "session", in)
	var p struct{ Approval string }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Approval == "" {
		t.Fatalf("preview: %d %s", w.Code, w.Body.String())
	}
	in.Approval = p.Approval
	for _, code := range []int{200, 409} {
		w = request(t, s, "POST", "/api/publish", s.origin, "session", in)
		if w.Code != code {
			t.Fatalf("expected %d, received %d %s", code, w.Code, w.Body.String())
		}
	}
	if calls != 1 {
		t.Fatalf("published %d times", calls)
	}
}
