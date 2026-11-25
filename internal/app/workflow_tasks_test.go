package app

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the normal GitHub client with only the runner token. The transport
// rejects every destination/method outside the single read in the fixture.
func TestWorkflowGitHubTaskReads(t *testing.T) {
	a, _ := issueApp(t)
	a.tasks = nil
	t.Setenv("GITHUB_REPOSITORY", "org/repo")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("JIRA_USER", "")
	t.Setenv("JIRA_TOKEN", "")
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	reads := 0
	http.DefaultTransport = docsPRTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		if r.Method != "GET" || r.URL.Host != "api.github.com" || r.URL.Path != "/repos/org/repo/issues/42" ||
			r.Header.Get("Authorization") != "Bearer fixture" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `{"number":42,"title":"Quota","state":"open"}`)
	})}
	msg := "fix: handle quota (#5)\n\nTask-Ref: github-issues/org%2Frepo/42"
	if err := a.ValidateWorkflowMessage(t.Context(), msg, true, false); err != nil {
		t.Fatal(err)
	}
	if issues := a.Validate(t.Context(), msg); len(issues) != 0 {
		t.Fatal(issues)
	}
	plan, err := a.PlanShippedTask(t.Context(), "org/repo#42", "pr.merged")
	if err != nil || plan.Applied || plan.To != "closed" || reads != 2 {
		t.Fatalf("plan=%+v, reads=%d, err=%v", plan, reads, err)
	}
	for _, key := range []string{"other/repo#42", "github-issues/org%2Frepo/0042"} {
		if _, err := a.PlanShippedTask(t.Context(), key, "pr.merged"); err == nil {
			t.Fatalf("accepted %s", key)
		}
	}
	t.Setenv("GITHUB_SERVER_URL", "https://git.example.invalid")
	if err := a.ValidateWorkflowMessage(t.Context(), msg, true, false); err == nil || reads != 2 {
		t.Fatalf("wrong token host: %v, reads=%d", err, reads)
	}
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	t.Setenv("GH_TOKEN", "")
	if err := a.ValidateWorkflowMessage(t.Context(), msg, true, false); err == nil || !strings.Contains(err.Error(), "GH_TOKEN") {
		t.Fatalf("missing token: %v", err)
	}
	if err := a.ValidateWorkflowMessage(t.Context(), msg+"\nTask-Ref: unused\nAgentflow: skip", true, false); err != nil || reads != 2 {
		t.Fatalf("opt out: %v, reads=%d", err, reads)
	}
}

func TestWorkflowJiraTaskReads(t *testing.T) {
	dir := newRepo(t)
	if err := os.Mkdir(filepath.Join(dir, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agentflow", "config.yml"), []byte("task_management: {provider: jira}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("JIRA_BASE", "https://jira.invalid")
	t.Setenv("JIRA_USER", "reader")
	t.Setenv("JIRA_TOKEN", "fixture")
	path, err := filepath.Abs("../../flow-map.yml")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLOW_MAP", path)
	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	http.DefaultTransport = docsPRTransport{http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "reader" || password != "fixture" || r.URL.Host != "jira.invalid" || r.Method != "GET" {
			t.Fatalf("unexpected runner request: %s %s", r.Method, r.URL)
		}
		switch r.URL.Path {
		case "/rest/api/3/issue/PLAT-12":
			fmt.Fprint(w, `{"key":"PLAT-12","fields":{"status":{"name":"To Do","statusCategory":{"key":"new"}}}}`)
		case "/rest/api/3/issue/PLAT-12/remotelink":
			fmt.Fprint(w, "[]")
		default:
			t.Fatalf("unexpected Jira path %q", r.URL.Path)
		}
	})}
	if err := a.ValidateWorkflowMessage(t.Context(), "fix(PLAT-12): handle quota (#5)", true, false); err != nil {
		t.Fatal(err)
	}
	plan, err := a.PlanShippedTask(t.Context(), "PLAT-12", "pr.opened")
	if err != nil || plan.From != "To Do" || plan.To == "" || plan.Applied {
		t.Fatalf("plan=%+v, err=%v", plan, err)
	}
}
