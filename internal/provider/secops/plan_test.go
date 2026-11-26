package secops_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/provider"
	"github.com/ryanhelms/agentflow/internal/provider/secops"
)

type replayTasks struct {
	provider.Tasks
	created map[provider.Ref]provider.TaskDraft
}

func (t replayTasks) Create(_ context.Context, draft provider.TaskDraft, options provider.WriteOptions) (provider.WriteResult, error) {
	ref := provider.Ref{Instance: "test-tasks", Scope: draft.Scope, ID: options.OperationID}
	if _, found := t.created[ref]; found {
		return provider.WriteResult{Ref: ref, Status: provider.AlreadyApplied}, nil
	}
	t.created[ref] = draft
	return provider.WriteResult{Ref: ref, Status: provider.Applied}, nil
}

func TestTaskReplay(t *testing.T) {
	ctx := t.Context()
	target := secops.TaskTarget{Instance: "test-tasks", Scope: "triage"}
	render := func(event secops.Event) (provider.TaskDraft, error) {
		return provider.TaskDraft{
			Title:       "[" + event.Severity + "] " + event.Title,
			Description: event.Body,
			Type:        string(event.Kind),
			Labels:      []string{"response"},
		}, nil
	}
	tasks := replayTasks{created: make(map[provider.Ref]provider.TaskDraft)}

	for _, kind := range []secops.GitHubAlertSource{secops.Dependabot, secops.CodeScanning} {
		source := githubSource(t, kind)
		checkReplay(t, tasks, func() (secops.TaskPlan, error) {
			findings, err := source.Findings(ctx, time.Time{})
			if err != nil {
				return secops.TaskPlan{}, err
			}
			if len(findings) == 0 {
				return secops.TaskPlan{}, errors.New("fixture produced no findings")
			}
			return secops.PlanFinding(findings[0], target, render)
		})
	}

	fixtures, err := secops.NewFixtureIncidents("response", "example/project", fixture(t, "incidents"))
	if err != nil {
		t.Fatal(err)
	}
	plan := checkReplay(t, tasks, func() (secops.TaskPlan, error) {
		incidents, err := fixtures.Incidents(ctx, time.Time{})
		if err != nil {
			return secops.TaskPlan{}, err
		}
		if len(incidents) == 0 {
			return secops.TaskPlan{}, errors.New("fixture produced no incidents")
		}
		return secops.PlanIncident(incidents[0], target, render)
	})
	if len(plan.Draft.Links) != 1 {
		t.Fatalf("incident task has %d links, want one source link", len(plan.Draft.Links))
	}
	link := plan.Draft.Links[0]
	if plan.Target != target || plan.Draft.Scope != target.Scope ||
		plan.Draft.Title != "[high] Service restoration" ||
		plan.Draft.Description != "The response team restored the service." ||
		plan.Draft.Type != "incident" || !reflect.DeepEqual(plan.Draft.Labels, []string{"response"}) ||
		link.Ref == nil || *link.Ref != (provider.Ref{Instance: "response", Scope: "example/project", ID: "INC-7"}) ||
		link.URL != "https://status.example.test/incidents/7" {
		t.Fatalf("incident task = %+v, source link = %+v", plan, link)
	}
	if len(tasks.created) != 3 {
		t.Fatalf("created %d tasks, want one per source event", len(tasks.created))
	}
}

func checkReplay(t *testing.T, tasks provider.Tasks, plan func() (secops.TaskPlan, error)) secops.TaskPlan {
	t.Helper()
	first, err := plan()
	if err != nil {
		t.Fatal(err)
	}
	created, err := tasks.Create(t.Context(), first.Draft, first.Options)
	if err != nil || created.Status != provider.Applied {
		t.Fatalf("first create = %+v, %v", created, err)
	}
	again, err := plan()
	if err != nil || !reflect.DeepEqual(first, again) {
		t.Fatalf("replay plan = %+v, %v, want %+v", again, err, first)
	}
	replayed, err := tasks.Create(t.Context(), again.Draft, again.Options)
	if err != nil || replayed.Status != provider.AlreadyApplied || replayed.Ref != created.Ref {
		t.Fatalf("replayed create = %+v, %v", replayed, err)
	}
	return first
}

func TestOperationIdentity(t *testing.T) {
	finding := provider.Finding{
		Ref:   provider.Ref{Instance: "github", Scope: "org/repo", ID: "dependabot:7"},
		Title: "Update a dependency", Severity: "high",
		URL: "https://github.com/org/repo/security/dependabot/7",
	}
	target := secops.TaskTarget{Instance: "tasks", Scope: "triage"}
	planFinding := func(finding provider.Finding, target secops.TaskTarget, render secops.TaskRenderer) secops.TaskPlan {
		t.Helper()
		plan, err := secops.PlanFinding(finding, target, render)
		if err != nil {
			t.Fatal(err)
		}
		return plan
	}
	first := planFinding(finding, target, nil)
	updated := finding
	updated.Title = "Updated dependency alert"
	updated.Severity = "critical"
	updated.URL += "?ref=main"
	replay := planFinding(updated, target, func(event secops.Event) (provider.TaskDraft, error) {
		return provider.TaskDraft{Title: "Triage " + event.Title, Type: "Bug"}, nil
	})
	if first.Options.OperationID == "" || replay.Options.OperationID != first.Options.OperationID || replay.Draft.Title == first.Draft.Title {
		t.Fatalf("updated event and template changed operation identity: %+v, %+v", first, replay)
	}
	differentRepo := finding
	differentRepo.Ref.Scope = "another-org/repo"
	incident, err := secops.PlanIncident(provider.Incident(finding), target, nil)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{first.Options.OperationID: true}
	for _, distinct := range []secops.TaskPlan{
		incident,
		planFinding(differentRepo, target, nil),
		planFinding(finding, secops.TaskTarget{Instance: "another-task-provider", Scope: target.Scope}, nil),
		planFinding(finding, secops.TaskTarget{Instance: target.Instance, Scope: "another-scope"}, nil),
	} {
		if seen[distinct.Options.OperationID] {
			t.Fatalf("operation ID reused for distinct plan: %+v", distinct)
		}
		seen[distinct.Options.OperationID] = true
	}
}

func TestPlanValidation(t *testing.T) {
	target := secops.TaskTarget{Instance: "tasks", Scope: "triage"}
	ref := provider.Ref{Instance: "response", Scope: "example/project", ID: "INC-7"}
	incident := provider.Incident{
		Ref: ref, Title: "Service restoration", Severity: "high",
		URL: "https://status.example.test/incidents/7",
	}
	renderError := errors.New("template failed")
	render := func(secops.Event) (provider.TaskDraft, error) { return provider.TaskDraft{}, renderError }
	if _, err := secops.PlanIncident(incident, target, render); !errors.Is(err, renderError) {
		t.Fatalf("plan error = %v, want %v", err, renderError)
	}
	if _, err := secops.PlanIncident(incident, secops.TaskTarget{}, nil); err == nil {
		t.Fatal("planned a task with an incomplete destination")
	}
	incident.URL = "https://user:password@status.example.test/incidents/7"
	if _, err := secops.PlanIncident(incident, target, nil); err == nil {
		t.Fatal("planned a task with credentials in its source link")
	}
}
