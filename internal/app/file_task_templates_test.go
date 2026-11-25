package app

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/tasktemplate"
)

const fileCatalog = `[{"name":"work","revision":"01",
  "defaults":{"scope":"eng","type":"Bug","priority":"High","title":"Triage","description":"Details",
             "labels":["triage"],"links":[{"url":"urn:guide"}]},
  "required":["scope","title","description","type","priority","labels","links"],
  "sections":[{"heading":"Objective","body":"Fix quota"}],"checklist":["Verify"]},
  {"name":"work","revision":"2","required":["title"]}]`

func TestFileTaskTemplateRoundTrip(t *testing.T) {
	for _, instance := range []string{"filesystem", "repository"} {
		t.Run(instance, func(t *testing.T) {
			ctx := t.Context()
			a := fileTaskApp(t, "  provider: "+instance+"\n  templates: {catalog: catalog.json, default: {name: work, revision: '01'}}\n")
			if instance == "repository" {
				bare := filepath.Join(t.TempDir(), "tasks.git")
				docsGit(t, a.Repo.Dir, "clone", "--bare", "--local", ".", bare)
				docsGit(t, bare, "branch", "board", "main")
				s := a.Cfg.Providers.TaskManagement.Settings.(*config.RepositoryTaskSettings)
				branch := "board"
				s.Repository, s.Branch, s.Publication = &bare, &branch, "push"
			}
			path := filepath.Join(a.Repo.Dir, "catalog.json")
			putCatalog := func(body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			putCatalog(fileCatalog)
			create := func(tok string) (any, error) { return a.CreateTicket(ctx, "", "", "", "", "", tok) }
			v, err := create("")
			if err != nil {
				t.Fatal(err)
			}
			token := confirmation(t, v)
			cfg := a.Cfg.Providers.TaskTemplates
			pin := *cfg.Default
			cfg.Default.Revision = "001"
			putCatalog(strings.Replace(fileCatalog, `"01"`, `"001"`, 1))
			if _, err = create(token); !errors.Is(err, policy.ErrToken) {
				t.Fatalf("approved different revision: %v", err)
			}
			*cfg.Default = pin
			putCatalog(strings.Replace(fileCatalog, "High", "Low", 1))
			if _, err = create(token); !errors.Is(err, policy.ErrToken) {
				t.Fatalf("approved different priority: %v", err)
			}
			putCatalog(fileCatalog)
			v, err = create(token)
			if err != nil {
				t.Fatal(err)
			}
			key := v.(map[string]string)["key"]
			ref, err := provider.ParseRef(key)
			if err != nil {
				t.Fatal(err)
			}
			task, err := a.getTask(ctx, ref)
			if err != nil || task.Template == nil || *task.Template != pin || ref.Instance != instance || ref.Scope != "eng" ||
				task.Title != "Triage" || task.Type != "Bug" || task.Priority != "High" || !slices.Equal(task.Labels, []string{"triage"}) ||
				len(task.Links) != 1 || task.Links[0].URL != "urn:guide" || task.Description != "Details\n\n## Objective\nFix quota\n\n- [ ] Verify" {
				t.Fatalf("stored task = %+v, err=%v", task, err)
			}
			cfg.Default.Revision = "2"
			plan, err := a.PlanTransition(ctx, key, "#done")
			if err != nil || plan.Template == nil || plan.Template.Ref != pin || plan.revision != task.Revision {
				t.Fatalf("transition plan = %+v, err=%v", plan, err)
			}
			a.Gate.Autonomy.Local = config.Confirm
			event := "#done"
			update := func(tok string) (any, error) { return a.Transition(ctx, key, event, false, tok) }
			v, err = update("")
			if err != nil {
				t.Fatal(err)
			}
			token = confirmation(t, v)
			putCatalog(strings.Replace(fileCatalog, `"01"`, `"1"`, 1))
			if _, err = update(token); err == nil || !strings.Contains(err.Error(), "unknown template") {
				t.Fatalf("lost pin accepted: %v", err)
			}
			// The same file revision can still fail validation at write time.
			putCatalog(strings.Replace(fileCatalog, `"Verify"`, `"Review"`, 1))
			if _, err = a.transitionFileTask(ctx, plan, token); err == nil || !strings.Contains(err.Error(), "checklist") {
				t.Fatalf("applied stale validation: %v", err)
			}
			current, err := a.getTask(ctx, ref)
			if err != nil || current.Revision != task.Revision {
				t.Fatalf("failed validation changed task = %+v, err=%v", current, err)
			}
			putCatalog(fileCatalog)
			// The failed write consumed approval for #done. Approve the other configured event.
			event = "pr.merged"
			v, err = update("")
			if err != nil {
				t.Fatal(err)
			}
			if _, err = update(confirmation(t, v)); err != nil {
				t.Fatal(err)
			}
			current, err = a.getTask(ctx, ref)
			if err != nil || current.Template == nil || *current.Template != pin || !current.State.Terminal || current.Revision == task.Revision {
				t.Fatalf("transitioned task = %+v, err=%v", current, err)
			}
		})
	}
}

func TestFileTaskWithoutPinIgnoresLaterDefault(t *testing.T) {
	a := fileTaskApp(t, "  provider: filesystem\n")
	ctx := t.Context()
	root := a.Cfg.Providers.TaskManagement.Settings.(*config.FileSystemTaskSettings).Root
	path := filepath.Join(a.Repo.Dir, root)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	const legacy = "---\nversion: 1\nkind: task\ninstance: filesystem\nscope: default\nid: t1\ntitle: Legacy\nstate: {id: open}\n---\n"
	if err := os.WriteFile(filepath.Join(path, "old.md"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	a.Cfg.Providers.TaskTemplates = &config.TaskTemplateSettings{Catalog: "missing.json", Default: &tasktemplate.Ref{Name: "work", Revision: "01"}}
	a.Gate.Autonomy.Local = config.Auto
	v, err := a.Transition(ctx, "filesystem/default/t1", "#done", false, "")
	if err != nil || !v.(TransitionResult).Applied || v.(TransitionResult).Template != nil {
		t.Fatalf("legacy transition = %+v, err=%v", v, err)
	}
}

func TestFileTaskTemplateInputsAndValidation(t *testing.T) {
	a := fileTaskApp(t, "  provider: filesystem\n  templates: {catalog: catalog.json}\n")
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, "catalog.json"), []byte(fileCatalog), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	pin := tasktemplate.Ref{Name: "work", Revision: "01"}
	link := provider.Ref{Instance: "filesystem", Scope: "custom", ID: "parent"}
	create := func(token string) (any, error) {
		return a.CreateTicketWithTemplate(ctx, "custom", "Feature", "Mine", "Keep me", link.String(), pin, token)
	}
	v, err := create("")
	if err != nil {
		t.Fatal(err)
	}
	v, err = create(confirmation(t, v))
	if err != nil {
		t.Fatal(err)
	}
	key := v.(map[string]string)["key"]
	ref, err := provider.ParseRef(key)
	if err != nil {
		t.Fatal(err)
	}
	task, err := a.getTask(ctx, ref)
	if err != nil || ref.Scope != "custom" || task.Title != "Mine" || task.Type != "Feature" ||
		!strings.HasPrefix(task.Description, "Keep me\n") || len(task.Links) != 1 || task.Links[0].Ref == nil || *task.Links[0].Ref != link {
		t.Fatalf("explicit inputs = %+v, err=%v", task, err)
	}
	path := filepath.Join(a.Repo.Dir, a.Cfg.Providers.TaskManagement.Settings.(*config.FileSystemTaskSettings).Root, "task-"+ref.StorageKey()+".md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, edit := range []struct{ from, to, want string }{
		{"- triage", "- ''", "labels"},
		{"priority: High", "priority: ''", "priority"},
		{"- [ ] Verify", "Verify later", "checklist"},
	} {
		if err := os.WriteFile(path, []byte(strings.Replace(string(raw), edit.from, edit.to, 1)), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := a.Transition(ctx, key, "#done", false, ""); err == nil || !strings.Contains(err.Error(), edit.want) {
			t.Fatalf("accepted edit to %s: %v", edit.want, err)
		}
		if plan, err := a.PlanTransition(ctx, key, "unmapped"); err != nil || plan.To != "" {
			t.Fatalf("unmapped event = %+v, err=%v", plan, err)
		}
	}
}
