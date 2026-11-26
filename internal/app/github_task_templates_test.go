package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/flowmap"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/tasktemplate"
)

const issueCatalog = `[
 {"name":"work","revision":"01","required":["scope","title","description","labels"],
  "defaults":{"scope":"Org/Backlog","title":"Triage","description":"Details","labels":["planned"]},
  "sections":[{"heading":"Objective","body":"Do the work"}],"checklist":["Verify"]},
 {"name":"work","revision":"2","required":["title"]},
 {"name":"bad","revision":"1","required":["priority"]},
 {"name":"bad","revision":"2","defaults":{"type":"Bug"}}
]`

func configureIssueTemplates(t *testing.T, a *App) *config.TaskTemplateSettings {
	t.Helper()
	settings := &config.TaskTemplateSettings{
		Catalog: "catalog.json", Default: &tasktemplate.Ref{Name: "work", Revision: "01"},
	}
	a.Cfg.Providers.TaskTemplates = settings
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, settings.Catalog), []byte(issueCatalog), 0600); err != nil {
		t.Fatal(err)
	}
	return settings
}

func TestIssueTemplateCreateApproval(t *testing.T) {
	a, f := issueApp(t)
	cfg := configureIssueTemplates(t, a)
	create := func(token string) (any, error) {
		return a.CreateTicket(t.Context(), "", "Task", "", "", "#42", token)
	}
	v, err := create("")
	if err != nil {
		t.Fatal(err)
	}
	token := confirmation(t, v)
	summary := v.(*Pending).Summary.(map[string]any)
	wantBody := "Details\n\n## Objective\nDo the work\n\n- [ ] Verify\n\nRelated issue: org/repo#42"
	if summary["description"] != wantBody || summary["summary"] != "Triage" || summary["project"] != "org/backlog" ||
		*summary["template"].(*tasktemplate.Ref) != *cfg.Default || !slices.Equal(summary["labels"].([]string), []string{"planned"}) {
		t.Fatalf("preview = %+v", summary)
	}
	for _, change := range [][2]string{{"Do the work", "New objective"}, {"planned", "other-label"}, {"Org/Backlog", "Org/Elsewhere"}} {
		catalog := strings.ReplaceAll(issueCatalog, change[0], change[1])
		if err := os.WriteFile(filepath.Join(a.Repo.Dir, cfg.Catalog), []byte(catalog), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := create(token); !errors.Is(err, policy.ErrToken) {
			t.Fatalf("approved changed %s: %v", change[0], err)
		}
	}
	configureIssueTemplates(t, a)
	// Change only the pin while leaving all resolved fields identical.
	path := filepath.Join(a.Repo.Dir, cfg.Catalog)
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(issueCatalog, `"01"`, `"001"`)), 0600); err != nil {
		t.Fatal(err)
	}
	a.Cfg.Providers.TaskTemplates.Default.Revision = "001"
	if _, err := create(token); !errors.Is(err, policy.ErrToken) || len(f.writes) != 0 {
		t.Fatalf("approved changed pin: writes=%v err=%v", f.writes, err)
	}
	configureIssueTemplates(t, a)
	if _, err := create(token); err != nil {
		t.Fatal(err)
	}
	if f.draft.Template == nil || *f.draft.Template != *cfg.Default || f.draft.Description != wantBody ||
		f.draft.Title != "Triage" || f.draft.Scope != "org/backlog" || !slices.Equal(f.draft.Labels, []string{"planned"}) {
		t.Fatalf("created %+v", f.draft)
	}
	selected := tasktemplate.Ref{Name: "work", Revision: "2"}
	explicit := func(token string) (any, error) {
		return a.CreateTicketWithTemplate(t.Context(), "Other/Repo", "", "Explicit", "Keep\r\n", "", selected, token)
	}
	v, err = explicit("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = explicit(confirmation(t, v)); err != nil {
		t.Fatal(err)
	}
	if *f.draft.Template != selected || f.draft.Title != "Explicit" || f.draft.Description != "Keep\r\n" ||
		f.draft.Scope != "other/repo" || !slices.Equal(f.draft.Labels, []string{"triage"}) {
		t.Fatalf("explicit draft = %+v", f.draft)
	}
}

func TestIssueCreateLinksConfiguredKnowledge(t *testing.T) {
	a, f := issueApp(t)
	configureIssueTemplates(t, a)
	a.Cfg.Providers.KnowledgeBase.Provider = "repository"
	const doc = "repository/engineering/provider-architecture"
	create := func(link, token string) (any, error) {
		return a.CreateTicket(t.Context(), "", "Task", "", "", link, token)
	}
	preview, err := create(doc, "")
	if err != nil {
		t.Fatal(err)
	}
	summary := preview.(*Pending).Summary.(map[string]any)
	description := summary["description"].(string)
	if !strings.Contains(description, "- [ ] Verify") || !strings.Contains(description, "## Related Knowledge\n\n- `"+doc+"`") || len(f.writes) != 0 {
		t.Fatalf("unexpected preview: %+v, writes %v", summary, f.writes)
	}
	token := confirmation(t, preview)
	if _, err := create("filesystem/engineering/provider-architecture", ""); err == nil {
		t.Fatal("accepted a nonselected knowledge provider")
	}
	if _, err := create("repository/engineering/other", token); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("accepted changed document under the original approval: %v", err)
	}
	if _, err := create(doc, token); err != nil || len(f.writes) != 1 || f.draft.Description != description {
		t.Fatalf("creation did not preserve the approved description: %+v, %v", f.draft, err)
	}
}

func TestIssueTemplateRejectsInvalidCreate(t *testing.T) {
	a, f := issueApp(t)
	configureIssueTemplates(t, a)
	for _, tc := range []struct {
		ref         tasktemplate.Ref
		want        string
		unsupported bool
	}{
		{tasktemplate.Ref{Name: "work", Revision: "2"}, "missing required field", false},
		{tasktemplate.Ref{Name: "work"}, "name and revision", false},
		{tasktemplate.Ref{Name: "bad", Revision: "1"}, "priority", true},
		{tasktemplate.Ref{Name: "bad", Revision: "2"}, "type", true},
	} {
		v, err := a.CreateTicketWithTemplate(t.Context(), "", "", "", "", "", tc.ref, "")
		if err == nil || !strings.Contains(err.Error(), tc.want) || errors.Is(err, provider.ErrUnsupported) != tc.unsupported || v != nil || len(f.writes) != 0 {
			t.Fatalf("%v: preview=%v writes=%v err=%v", tc.ref, v, f.writes, err)
		}
	}
}

func TestIssueTemplateUpdate(t *testing.T) {
	for _, op := range []string{"comment", "transition"} {
		t.Run(op, func(t *testing.T) {
			a, f := issueApp(t)
			cfg := configureIssueTemplates(t, a)
			pin := *cfg.Default
			cfg.Default = &tasktemplate.Ref{Name: "bad", Revision: "1"}
			f.task.Template = &pin
			f.task.Description = "## Objective\nUser edit\n\n- [x] Verify"
			f.task.Labels = []string{"current"}
			update := func(token string) (any, error) {
				if op == "comment" {
					return a.Comment(t.Context(), "#42", "Progress", token)
				}
				return a.Transition(t.Context(), "#42", flowmap.PRMerged, false, token)
			}
			v, err := update("")
			if err != nil {
				t.Fatal(err)
			}
			token := confirmation(t, v)
			f.task.Labels = nil
			if _, err = update(token); err == nil || !strings.Contains(err.Error(), "labels") {
				t.Fatalf("accepted missing current labels: %v", err)
			}
			f.task.Labels = []string{"current"}
			body := f.task.Description
			f.task.Description = "## Objective\nEdited"
			if _, err = update(token); err == nil || !strings.Contains(err.Error(), "checklist") {
				t.Fatalf("accepted missing checklist: %v", err)
			}
			f.task.Description = body + "\nmore"
			if _, err = update(token); !errors.Is(err, policy.ErrToken) {
				t.Fatalf("accepted changed content: %v", err)
			}
			f.task.Description = body
			f.task.Template = &tasktemplate.Ref{Name: "work", Revision: "2"}
			if _, err = update(token); !errors.Is(err, policy.ErrToken) {
				t.Fatalf("accepted changed pin: %v", err)
			}
			f.task.Template = cfg.Default
			if _, err = update(token); !errors.Is(err, provider.ErrUnsupported) {
				t.Fatalf("accepted unsupported pin: %v", err)
			}
			f.task.Template = &tasktemplate.Ref{Name: "removed", Revision: "1"}
			if _, err = update(token); err == nil || !strings.Contains(err.Error(), "unknown template") {
				t.Fatalf("accepted missing pin: %v", err)
			}
			f.task.Template = &pin
			if len(f.writes) != 0 {
				t.Fatalf("unexpected writes: %v", f.writes)
			}
			if _, err = update(token); err != nil || len(f.writes) != 1 {
				t.Fatalf("update: writes=%v err=%v", f.writes, err)
			}
			f.task.Template = nil
			f.task.Description = ""
			f.task.Labels = nil
			f.task.State = provider.TaskState{ID: "open"}
			cfg.Catalog = "absent.json"
			a.Gate.Autonomy.Remote = config.Auto
			if _, err = update(""); err != nil || len(f.writes) != 2 {
				t.Fatalf("legacy update: writes=%v err=%v", f.writes, err)
			}
		})
	}
}

func TestIssueTemplateRechecksBeforeApplyingPlan(t *testing.T) {
	a, f := issueApp(t)
	cfg := configureIssueTemplates(t, a)
	f.task.Template = cfg.Default
	f.task.Description = "## Objective\nWork\n\n- [ ] Verify"
	f.task.Labels = []string{"planned"}
	plan, err := a.PlanTransition(t.Context(), "#42", flowmap.PRMerged)
	if err != nil {
		t.Fatal(err)
	}
	f.task.Template = nil
	if _, err := a.ApplyTransition(t.Context(), plan); !errors.Is(err, provider.ErrConflict) || len(f.writes) != 0 {
		t.Fatalf("stale plan: writes=%v err=%v", f.writes, err)
	}
}
