package webui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/settings"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// fakeSettings records what the endpoints hand the backend.
type fakeSettings struct {
	calls  []string
	tokens []string
	err    error
}

func (f *fakeSettings) Catalog(_ context.Context, c SettingsCall) (any, error) {
	f.calls = append(f.calls, "catalog:"+string(c.Worktree))
	return settings.View{CatalogRevision: "sha256:x"}, f.err
}

func (f *fakeSettings) Preview(_ context.Context, _ SettingsCall, cs settings.ChangeSet) (any, error) {
	f.calls = append(f.calls, "preview:"+cs.Edits[0].ID)
	return settings.Preview{Status: settings.StatusOK}, f.err
}

func (f *fakeSettings) Save(_ context.Context, _ SettingsCall, cs settings.ChangeSet, token string) (any, error) {
	f.calls = append(f.calls, "save:"+cs.Edits[0].ID)
	f.tokens = append(f.tokens, token)
	if token == "" {
		return &app.Pending{Status: "needs_confirmation", ConfirmToken: "cf_token", Reason: "review", Summary: map[string]string{"operation": "config.set"}}, nil
	}
	return settings.SaveResult{Status: settings.StatusSaved, Operation: "op_1"}, f.err
}

func (f *fakeSettings) Undo(_ context.Context, _ SettingsCall, _ settings.UndoRequest, token string) (any, error) {
	f.calls = append(f.calls, "undo")
	return nil, f.err
}

func TestSettingsEndpoints(t *testing.T) {
	s := testServer(t)
	s.worktreePath = "/w/main/"
	fake := &fakeSettings{}
	RegisterSettingsBackend(fake)
	t.Cleanup(func() { RegisterSettingsBackend(nil) })
	edit := settings.ChangeSet{Edits: []settings.Edit{{ID: "personal:autonomy.local", Op: settings.OpSet, Value: json.RawMessage(`"auto"`)}}}

	// The session, origin and request-header checks hold.
	if w := request(t, s, "POST", "/api/settings/save", s.origin, "", map[string]any{"change_set": edit}); w.Code != 401 {
		t.Fatalf("unauthenticated save: %d", w.Code)
	}
	if w := request(t, s, "POST", "/api/settings/preview", "http://evil.example", "session", edit); w.Code != 403 {
		t.Fatalf("foreign origin: %d", w.Code)
	}

	if w := request(t, s, "GET", "/api/settings/catalog", s.origin, "session", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "sha256:x") {
		t.Fatalf("catalog: %d %s", w.Code, w.Body)
	}
	if w := request(t, s, "POST", "/api/settings/preview", s.origin, "session", edit); w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"ok"`) {
		t.Fatalf("preview: %d %s", w.Code, w.Body)
	}
	w := request(t, s, "POST", "/api/settings/save", s.origin, "session", map[string]any{"change_set": edit})
	var pending map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &pending)
	if w.Code != 200 || pending["approval"] != "cf_token" || pending["confirm_token"] != nil || pending["status"] != "needs_confirmation" {
		t.Fatalf("first save: %d %s", w.Code, w.Body)
	}
	w = request(t, s, "POST", "/api/settings/save", s.origin, "session", map[string]any{"change_set": edit, "approval": "cf_token"})
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"saved"`) || fake.tokens[1] != "cf_token" {
		t.Fatalf("confirmed save: %d %s", w.Code, w.Body)
	}
	fake.err = settings.ErrNothingToUndo
	if w := request(t, s, "POST", "/api/settings/undo", s.origin, "session", map[string]any{"undo": map[string]any{}}); w.Code != 404 {
		t.Fatalf("undo without a record: %d %s", w.Code, w.Body)
	}
	fake.err = errors.Join(policy.ErrChanged)
	if w := request(t, s, "POST", "/api/settings/undo", s.origin, "session", map[string]any{"undo": map[string]any{}}); w.Code != 409 || !strings.Contains(w.Body.String(), "operation_changed") {
		t.Fatalf("changed: %d %s", w.Code, w.Body)
	}
	if fake.calls[0] != "catalog:main" {
		t.Errorf("calls %v", fake.calls)
	}
}

func TestSettingsProjection(t *testing.T) {
	s := testServer(t)
	home := os.Getenv("AGENTFLOW_HOME")
	if err := os.WriteFile(filepath.Join(home, "config.yml"), []byte("# private\nautonomy:\n  local: auto\natlassian:\n  email: me@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := settingsStateOf(t.Context(), s.repo.Dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" || !st.Protected || st.Revisions.Personal == settings.RevisionAbsent || st.CatalogRevision == "" || st.LastSave != nil {
		t.Fatalf("state %+v", st)
	}
	raw, _ := json.Marshal(st)
	if strings.Contains(string(raw), "# private") {
		t.Errorf("the state carries personal YAML")
	}
	var local *settings.FieldValue
	for i, v := range st.Values {
		if v.ID == "personal:autonomy.local" {
			local = &st.Values[i]
		}
	}
	if local == nil || local.Source != settings.SourcePersonal || string(local.Layer.JSON) != `"auto"` {
		t.Fatalf("personal:autonomy.local %+v", local)
	}

	r := &repoEvents{id: "0123456789abcdef", worktrees: map[bus.WorktreeID]registeredWorktree{"main": {ID: "main", Root: s.repo.Dir}}}
	p := &settingsProjection{repo: r}
	op := func(name string) bus.Event {
		payload, _ := json.Marshal(bus.OpPayload{Operation: name})
		return bus.Event{Type: bus.TypeOpApplied, Repo: r.id, Worktree: "main", Payload: payload}
	}
	// The pull request state comes from the GitHub poller's cache.
	if got, ok := p.pullState(7); ok || got != "" {
		t.Errorf("no poller: %q %v", got, ok)
	}
	gh := &fakeGitHub{states: map[bus.RepoID]watch.GitHubState{r.id: {Pulls: []bus.GHPullUpdated{{Number: 7, State: bus.PullMerged}}}}}
	r.github = func() watch.GitHubCache { return gh }
	if got, ok := p.pullState(7); !ok || got != "merged" {
		t.Errorf("pull 7: %q %v", got, ok)
	}
	if _, ok := p.pullState(8); ok {
		t.Errorf("pull 8 is not in the cache")
	}
	for _, tc := range []struct {
		e    bus.Event
		keys int
	}{
		{op("config.set"), 1},
		{op("commit"), 0},
		{bus.Event{Type: bus.TypeConfigLayerChanged, Repo: bus.GlobalRepo}, 1},
		{bus.Event{Type: bus.TypeGitHeadChanged, Repo: r.id, Worktree: "main"}, 1},
		{bus.Event{Type: bus.TypeGitHeadChanged, Repo: "fedcba9876543210", Worktree: "main"}, 0},
	} {
		if got := len(p.Keys(tc.e)); got != tc.keys {
			t.Errorf("%s %s: %d keys, want %d", tc.e.Type, tc.e.Repo, got, tc.keys)
		}
	}
}

// rawSettingsRequest sends body as is, with the session and origin, and the
// given headers.
func rawSettingsRequest(t *testing.T, s *server, path string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", s.origin+path, bytes.NewReader(body))
	req.Header.Set("Origin", s.origin)
	req.Header.Set("Authorization", "Bearer "+grantToken(s.session, "tab/v1", time.Now().Add(time.Hour)))
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

func TestSettingsEndpointLimits(t *testing.T) {
	s := testServer(t)
	s.worktreePath = "/w/main/"
	fake := &fakeSettings{}
	RegisterSettingsBackend(fake)
	t.Cleanup(func() { RegisterSettingsBackend(nil) })
	body := []byte(`{"edits":[{"id":"personal:autonomy.local","op":"set","value":"auto"}]}`)
	console := map[string]string{"Content-Type": "application/json", "X-Agentflow-Request": "console"}
	with := func(k, v string) map[string]string {
		h := map[string]string{}
		for a, b := range console {
			h[a] = b
		}
		h[k] = v
		return h
	}
	if w := rawSettingsRequest(t, s, "/api/settings/preview", body, console); w.Code != 200 {
		t.Fatalf("well-formed request: %d %s", w.Code, w.Body)
	}
	if w := rawSettingsRequest(t, s, "/api/settings/save", body, with("X-Agentflow-Request", "")); w.Code != 403 {
		t.Errorf("missing X-Agentflow-Request: %d", w.Code)
	}
	if w := rawSettingsRequest(t, s, "/api/settings/preview", body, with("Content-Type", "text/plain")); w.Code != 415 {
		t.Errorf("wrong content type: %d", w.Code)
	}
	big := []byte(`{"edits":[{"id":"personal:autonomy.local","op":"set","value":"` + strings.Repeat("a", MaxSettingsBody) + `"}]}`)
	if w := rawSettingsRequest(t, s, "/api/settings/preview", big, console); w.Code != 413 {
		t.Errorf("oversized body: %d", w.Code)
	}
	// Without a length the limit still holds while reading.
	req := httptest.NewRequest("POST", s.origin+"/api/settings/save", io.MultiReader(bytes.NewReader(big)))
	req.ContentLength = -1
	req.Header.Set("Origin", s.origin)
	req.Header.Set("Authorization", "Bearer "+grantToken(s.session, "tab/v1", time.Now().Add(time.Hour)))
	for k, v := range console {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 413 {
		t.Errorf("oversized body without a length: %d", w.Code)
	}
	if len(fake.calls) != 1 {
		t.Errorf("refused requests reached the backend: %v", fake.calls)
	}

	// A failure is a fixed code and message; the detail goes to the log.
	var log bytes.Buffer
	old := settingsLog
	settingsLog = &log
	t.Cleanup(func() { settingsLog = old })
	fake.err = errors.New("yaml: line 3: mapping values are not allowed in /home/me/.agentflow/config.yml")
	w = rawSettingsRequest(t, s, "/api/settings/preview", body, console)
	if w.Code != 422 || strings.Contains(w.Body.String(), "yaml") || !strings.Contains(w.Body.String(), CodeSettingsFailed) {
		t.Errorf("failure: %d %s", w.Code, w.Body)
	}
	if !strings.Contains(log.String(), "mapping values") {
		t.Errorf("log %q", log.String())
	}
}
