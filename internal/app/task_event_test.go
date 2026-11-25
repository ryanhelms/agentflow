package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/event"
	"github.com/nscaledev/agentflow/internal/provider"
)

func TestApplyGitHubTaskEventReplay(t *testing.T) {
	a, fake := issueApp(t)
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	fake.task.Ref.Scope, fake.task.Ref.ID = "nscaledev/agentflow", "25"
	*a.issueSettings().Repository = fake.task.Ref.Scope
	a.Gate.Autonomy.Remote = config.Auto
	metadata := event.GitHubActionsContext{
		Repository:   provider.RepositoryRef{Host: "github.com", Path: fake.task.Ref.Scope},
		RepositoryID: 100, DefaultBranch: "main", EventName: "pull_request", EventID: "test-run", ApprovedIssue: fake.task.Ref.ID,
	}
	raw, err := os.ReadFile("../event/testdata/pr-merged.json")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(raw), "acme/widget", fake.task.Ref.Scope)
	body = strings.ReplaceAll(body, "issues/acme%2Fwidget/42", fake.task.Ref.String())
	payload := filepath.Join(t.TempDir(), "pull-request.json")
	writePayload := func(text string) {
		t.Helper()
		if err := os.WriteFile(payload, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writePayload(body)
	preview, err := a.ApplyGitHubTaskEvent(t.Context(), metadata, payload, true)
	if err != nil || len(preview.Effects) != 1 || preview.Effects[0].To != "closed" || len(fake.writes) != 0 {
		t.Fatalf("preview = %+v, writes = %v, err = %v", preview, fake.writes, err)
	}
	wrong := metadata
	wrong.ApprovedIssue = "57"
	if _, err := a.ApplyGitHubTaskEvent(t.Context(), wrong, payload, false); err == nil || len(fake.writes) != 0 {
		t.Fatalf("write attempted for an unapproved issue: %v, %v", fake.writes, err)
	}
	// Even when the enrolled issue is first, a second issue disqualifies the whole merge.
	second := fake.task.Ref
	second.ID = "26"
	writePayload(strings.Replace(body, fake.task.Ref.String(), fake.task.Ref.String()+`\nTask-Ref: `+second.String(), 1))
	if _, err := a.ApplyGitHubTaskEvent(t.Context(), metadata, payload, false); err == nil || len(fake.writes) != 0 {
		t.Fatalf("multi-issue merge: writes = %v, err = %v", fake.writes, err)
	}
	writePayload(body)
	applied, err := a.ApplyGitHubTaskEvent(t.Context(), metadata, payload, false)
	if err != nil || len(applied.Effects) != 1 || !applied.Effects[0].Applied || len(fake.writes) != 1 {
		t.Fatalf("apply = %+v, writes = %v, err = %v", applied, fake.writes, err)
	}
	intent := preview.Plan.Transitions[0]
	if intent.Task != fake.task.Ref || intent.OperationID == "" || applied.Plan.Transitions[0] != intent || fake.writes[0] != intent.OperationID {
		t.Fatalf("preview = %+v, apply = %+v, writes = %v", intent, applied.Plan.Transitions, fake.writes)
	}
	replay, err := a.ApplyGitHubTaskEvent(t.Context(), metadata, payload, false)
	if err != nil || len(replay.Effects) != 1 || replay.Effects[0].Applied || len(fake.writes) != 1 {
		t.Fatalf("replay = %+v, writes = %v, err = %v", replay, fake.writes, err)
	}
	if replay.Plan.Transitions[0] != intent || replay.Effects[0].Key != applied.Effects[0].Key || replay.Effects[0].From != "closed" {
		t.Fatalf("replay did not observe the closed issue: %+v", replay)
	}
}

func TestConsumerTaskEventCannotApply(t *testing.T) {
	a, f := issueApp(t)
	a.Gate.Autonomy.Remote = config.Auto
	metadata := event.GitHubActionsContext{Repository: provider.RepositoryRef{Host: "github.com", Path: f.task.Ref.Scope}, RepositoryID: 100, DefaultBranch: "main", EventName: "pull_request", EventID: "10", ApprovedIssue: f.task.Ref.ID}
	raw, err := os.ReadFile("../event/testdata/pr-merged.json")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.ReplaceAll(string(raw), "acme/widget", f.task.Ref.Scope)
	body = strings.ReplaceAll(body, "issues/acme%2Fwidget/42", f.task.Ref.String())
	path := filepath.Join(t.TempDir(), "pr.json")
	if err = os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = a.ApplyGitHubTaskEvent(t.Context(), metadata, path, false); err == nil || !strings.Contains(err.Error(), "approved nscaledev/agentflow") || len(f.writes) > 0 {
		t.Fatalf("consumer apply: writes=%v, err=%v", f.writes, err)
	}
}
