package app

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/githubissues"
)

func TestSecOpsTriageReconcilesAmbiguousIssueCreate(t *testing.T) {
	dir := newRepo(t)
	t.Setenv("AGENTFLOW_SECOPS_TOKEN", "")
	t.Setenv("GH_TOKEN", "fixture")
	readFixture := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("../provider/secops/testdata", name+".json"))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	findings := map[string][]byte{
		"/repos/example/project/dependabot/alerts":    readFixture("dependabot"),
		"/repos/example/project/code-scanning/alerts": readFixture("code-scanning"),
	}
	var mu sync.Mutex
	var body string
	var posts int
	visible := false
	transport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = transport })
	http.DefaultTransport = docsPRTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Host != "api.github.com" {
			t.Errorf("unexpected fixture host %s", r.URL.Host)
		}
		switch {
		case r.Method == http.MethodGet && findings[r.URL.Path] != nil:
			if r.URL.Query().Get("state") != "open" {
				t.Errorf("unexpected finding query %s", r.URL)
			}
			_, _ = w.Write(findings[r.URL.Path])
		case r.URL.Path == "/repos/example/triage/issues" && r.Method == http.MethodGet:
			if !visible {
				_, _ = io.WriteString(w, "[]")
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"number": 42, "title": "Update the parser dependency", "body": body, "state": "open",
				"html_url": "https://example.test/example/triage/issues/42",
			}})
		case r.URL.Path == "/repos/example/triage/issues" && r.Method == http.MethodPost:
			posts++
			var draft struct {
				Body   string
				Labels []string
			}
			if err := json.NewDecoder(r.Body).Decode(&draft); err != nil {
				t.Errorf("decode draft: %v", err)
			}
			body = draft.Body
			if len(draft.Labels) != 1 || draft.Labels[0] != "security" {
				t.Errorf("unexpected labels %v", draft.Labels)
			}
			http.Error(w, "write response lost", http.StatusBadGateway)
		default:
			t.Errorf("unexpected fixture request %s %s", r.Method, r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})}

	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(name, data string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, ".agentflow", name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	configure := func(revision string) *App {
		t.Helper()
		write("config.yml", `secops:
  settings:
    repository: Example/Project
    incidents: {fixture: .agentflow/incidents.json, scope: example/project}
task_management:
  provider: github-issues
  settings: {repository: example/triage}
  templates:
    catalog: .agentflow/templates.json
    default: {name: security, revision: "`+revision+`"}
`)
		a, err := Open(t.Context(), dir, "test")
		if err != nil {
			t.Fatal(err)
		}
		a.Gate.Autonomy.Remote = config.Auto
		return a
	}
	write("incidents.json", string(readFixture("incidents")))
	write("templates.json", `[
  {"name":"security","revision":"1","defaults":{"labels":["security"]},"checklist":["Triage"]},
  {"name":"security","revision":"2","defaults":{"labels":["security"]},"checklist":["Review"]}
]`)
	a := configure("1")
	out, err := a.SecOpsTriage(t.Context(), SecOpsTriageInput{}, "")
	plans, ok := out.([]SecOpsTaskPlan)
	if err != nil || !ok || len(plans) != 3 {
		t.Fatalf("preview = %+v, %v", out, err)
	}
	plan := plans[0]
	if plan.Task.Title != "Update the parser dependency" || plan.Task.Scope != "example/triage" ||
		plan.Severity != "high" || plan.OperationID == plans[1].OperationID || plan.OperationID == plans[2].OperationID ||
		!strings.Contains(plan.Task.Description, "<https://github.com/Example/Project/security/dependabot/7>") ||
		!strings.Contains(plan.Task.Description, "- [ ] Triage") {
		t.Fatalf("finding plan = %+v", plan)
	}
	source, err := provider.ParseRef(plan.Source)
	if err != nil {
		t.Fatal(err)
	}
	input := SecOpsTriageInput{Source: source, Apply: true}
	confirm := func(a *App) string {
		t.Helper()
		out, err := a.SecOpsTriage(t.Context(), input, "")
		if err != nil {
			t.Fatal(err)
		}
		return confirmation(t, out)
	}
	token := confirm(a)
	mu.Lock()
	n := posts
	mu.Unlock()
	if n != 0 {
		t.Fatalf("preview and confirmation sent %d POSTs", n)
	}
	if _, err := a.SecOpsTriage(t.Context(), input, token); !errors.Is(err, githubissues.ErrWriteOutcomeUnknown) {
		t.Fatalf("ambiguous create = %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Home, "secops-operations", plan.OperationID)); err != nil {
		t.Fatalf("claim missing: %v", err)
	}
	a = configure("2")
	if _, err := a.SecOpsTriage(t.Context(), input, confirm(a)); !errors.Is(err, githubissues.ErrWriteOutcomeUnknown) {
		t.Fatalf("replay without visible receipt = %v", err)
	}
	mu.Lock()
	visible = true
	mu.Unlock()
	a = configure("2")
	out, err = a.SecOpsTriage(t.Context(), input, confirm(a))
	result, ok := out.(provider.WriteResult)
	if err != nil || !ok || result.Status != provider.AlreadyApplied || result.Ref.ID != "42" {
		t.Fatalf("reconciled result = %+v, %v", out, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 || !strings.Contains(body, "- [ ] Triage") || !strings.Contains(body, "security/dependabot/7") {
		t.Fatalf("POSTs = %d, persisted body = %q", posts, body)
	}
}
