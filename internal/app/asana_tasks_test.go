package app

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/asana"
)

type asanaTransport func(*http.Request) (*http.Response, error)

func (f asanaTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAsanaProjectOperations(t *testing.T) {
	ctx := t.Context()
	a := fileTaskApp(t, `  provider: asana
  settings:
    project: "10"
    transitions:
      "10": {pr.opened: "12", "#done": completed}
      "20": {pr.opened: "22"}
knowledge_base: {provider: filesystem, settings: {root: docs}}
`)
	if err := os.Mkdir(filepath.Join(a.Repo.Dir, "docs"), 0700); err != nil {
		t.Fatal(err)
	}
	doc := "---\nversion: 1\nkind: document\ninstance: filesystem\nscope: team\nid: design\ntitle: Design Review\n---\nAn independent record.\n"
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, "docs", "design.md"), []byte(doc), 0600); err != nil {
		t.Fatal(err)
	}
	const task = `{"gid":"101","name":"Design","notes":"devflow-task-v1: {\"links\":[{\"ref\":{\"instance\":\"filesystem\",\"scope\":\"team\",\"id\":\"design\"}}]}\n\n- [ ] Test it","completed":false,"memberships":[{"project":{"gid":"10"},"section":{"gid":"11","name":"Ready"}},{"project":{"gid":"20"},"section":{"gid":"21","name":"Later"}}]}`
	writes := 0
	a.tasks, _ = asana.New(&http.Client{Transport: asanaTransport(func(r *http.Request) (*http.Response, error) {
		rec := httptest.NewRecorder()
		switch r.Method + " " + r.URL.Path {
		case "GET /api/1.0/tasks/101":
			_, _ = rec.WriteString(`{"data":` + task + `}`)
		case "GET /api/1.0/projects/10/tasks":
			_, _ = rec.WriteString(`{"data":[` + task + `]}`)
		case "GET /api/1.0/sections/12":
			_, _ = rec.WriteString(`{"data":{"gid":"12","project":{"gid":"10"}}}`)
		case "POST /api/1.0/sections/12/addTask":
			writes++
			_, _ = rec.WriteString(`{"data":{}}`)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL)
		}
		return rec.Result(), nil
	})}, "fake", a.asanaSettings().AdapterConfig())
	work, err := a.MyWork(ctx)
	if err != nil || len(work) != 1 || work[0].Key != "asana/10/101" {
		t.Fatalf("work %+v, %v", work, err)
	}
	tc, err := a.Context(ctx, "101")
	if err != nil || tc.Key != work[0].Key || tc.Status != "Ready" || tc.FlowNext["pr.opened"] != "12" || len(tc.Docs) != 1 || tc.Docs[0].Title != "Design Review" || tc.Docs[0].ID != "filesystem/team/design" || tc.Docs[0].URL != "" || !strings.Contains(tc.Text, "Doc: Design Review filesystem/team/design") || len(tc.Warnings) != 0 {
		t.Fatalf("context %+v, %v", tc, err)
	}
	tc, err = a.Context(ctx, "asana/20/101")
	if err != nil || tc.Status != "Later" || tc.FlowNext["pr.opened"] != "22" {
		t.Fatalf("other project %+v, %v", tc, err)
	}
	if _, err := a.Context(ctx, "asana/30/101"); err == nil {
		t.Fatal("accepted unconfigured project")
	}
	a.Gate.Autonomy.Remote = config.Confirm
	out, err := a.Transition(ctx, "101", "pr.opened", false, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := confirmation(t, out)
	if _, err = a.Transition(ctx, "asana/20/101", "pr.opened", false, tok); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("cross-project approval: %v", err)
	}
	out, err = a.Transition(ctx, "101", "pr.opened", false, tok)
	if err != nil || !out.(TransitionResult).Applied || writes != 1 {
		t.Fatalf("transition %+v, %v", out, err)
	}
	if _, err = a.Comment(ctx, "101", "test", ""); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatal(err)
	}
	if _, err = a.PostEOD(ctx, map[string]string{"101": "done"}, ""); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatal(err)
	}
	// Start has only a local effect when no branch.created mapping exists.
	mustGit(t, a.Repo, "add", ".")
	mustGit(t, a.Repo, "commit", "-qm", "chore: configure providers")
	a.Gate.Autonomy.Local = config.Confirm
	out, err = a.Start(ctx, "101", false, "")
	if err != nil {
		t.Fatal(err)
	}
	out, err = a.Start(ctx, "101", false, confirmation(t, out))
	if err != nil || out.(StartResult).Assigned || a.CurrentKey(ctx) != "asana/10/101" {
		t.Fatalf("start %+v, %v", out, err)
	}
	body, err := a.taskPRBody(ctx, "Design", a.CurrentKey(ctx))
	if err != nil || !strings.Contains(body, "Task-Ref: asana/10/101") {
		t.Fatalf("PR body %q, %v", body, err)
	}
	// Asana preserves link metadata in context even if the record is missing.
	a.Cfg.Providers.KnowledgeBase.Settings.(*config.FileSystemSettings).Root = t.TempDir()
	tc, err = a.Context(ctx, "101")
	if err != nil || len(tc.Warnings) != 1 || len(tc.Docs) != 1 || tc.Docs[0].ID != "filesystem/team/design" || tc.Docs[0].Title != "design" {
		t.Fatalf("unresolved Asana link: %+v, %v", tc, err)
	}
}

func TestAsanaTemplateAtApprovalAndTransition(t *testing.T) {
	a := fileTaskApp(t, `  provider: asana
  settings: {project: "10", transitions: {"10": {"#qa": "12"}}}
  templates: {catalog: catalog.json, default: {name: plan, revision: "01"}}
knowledge_base: {provider: filesystem}
`)
	t.Setenv("DEVFLOW_OPERATION_ID", "")
	path := filepath.Join(a.Repo.Dir, "catalog.json")
	const catalog = `[{"name":"plan","revision":"01","required":["title","links"],"defaults":{"title":"Design","links":[{"ref":{"instance":"filesystem","scope":"team","id":"doc"}}]},"checklist":["Review"]},{"name":"plan","revision":"2","required":["priority"]}]`
	if err := os.WriteFile(path, []byte(catalog), 0600); err != nil {
		t.Fatal(err)
	}
	f := &fakeIssues{}
	a.tasks = f
	create := func(token string) (any, error) { return a.CreateTicket(t.Context(), "", "", "Override", "", "", token) }
	v, err := create("")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	if err := os.WriteFile(path, []byte(strings.Replace(catalog, "Review", "Approve", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := create(token); !errors.Is(err, policy.ErrToken) || len(f.writes) != 0 {
		t.Fatalf("changed draft: %v, %v", err, f.writes)
	}
	if err := os.WriteFile(path, []byte(catalog), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := create(token); err != nil {
		t.Fatal(err)
	}
	d := f.draft
	if d.Template == nil || d.Title != "Override" || len(d.Links) != 1 || d.Links[0].Ref.Instance != "filesystem" || d.Description != "- [ ] Review" {
		t.Fatalf("draft %+v", d)
	}
	f.task = provider.Task{Ref: provider.Ref{Instance: "asana", Scope: d.Scope, ID: "123"}, Title: d.Title, Template: d.Template, Description: "Edited in Asana\n- [x] Review", Links: d.Links, State: provider.TaskState{ID: "11"}}
	a.Cfg.Providers.TaskTemplates.Default.Revision = "2"
	if _, err := create(""); !errors.Is(err, provider.ErrUnsupported) {
		t.Fatalf("accepted required priority: %v", err)
	}
	plan, err := a.PlanTransition(t.Context(), "123", "#qa")
	if err != nil || plan.Template == nil || plan.Template.Revision != "01" || plan.To != "12" {
		t.Fatalf("stored pin plan %+v, %v", plan, err)
	}
	f.task.Description = "Edited in Asana"
	if _, err := a.PlanTransition(t.Context(), "123", "#qa"); err == nil || !strings.Contains(err.Error(), "missing required checklist") {
		t.Fatalf("invalid stored task: %v", err)
	}
	f.task.Template = nil
	plan, err = a.PlanTransition(t.Context(), "123", "#qa")
	if err != nil || plan.Template != nil || plan.To != "12" {
		t.Fatalf("native task %+v, %v", plan, err)
	}
}
