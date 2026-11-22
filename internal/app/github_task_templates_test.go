package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/tasktemplate"
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
