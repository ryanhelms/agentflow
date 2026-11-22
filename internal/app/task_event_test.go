package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/event"
	"github.com/nscaledev/devflow/internal/provider"
)

func TestApplyGitHubTaskEventReplay(t *testing.T) {
	a, fake := issueApp(t)
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("GITHUB_SERVER_URL", "https://github.com")
	fake.task.Ref.Scope, fake.task.Ref.ID = "nscaledev/devflow", "25"
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
	if err := os.WriteFile(payload, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	preview, err := a.ApplyGitHubTaskEvent(t.Context(), metadata, payload, true)
	if err != nil || len(preview.Effects) != 1 || preview.Effects[0].To != "closed" || len(fake.writes) != 0 {
		t.Fatalf("preview = %+v, writes = %v, err = %v", preview, fake.writes, err)
	}
	wrong := metadata
	wrong.ApprovedIssue = "57"
	if _, err := a.ApplyGitHubTaskEvent(t.Context(), wrong, payload, false); err == nil || len(fake.writes) != 0 {
		t.Fatalf("write attempted for an unapproved issue: %v, %v", fake.writes, err)
	}
	applied, err := a.ApplyGitHubTaskEvent(t.Context(), metadata, payload, false)
	if err != nil || len(applied.Effects) != 1 || !applied.Effects[0].Applied || len(fake.writes) != 1 {
		t.Fatalf("apply = %+v, writes = %v, err = %v", applied, fake.writes, err)
	}
	if expected := applied.Plan.Transitions[0].OperationID; fake.writes[0] != expected {
		t.Fatalf("operation ID = %q, want %q", fake.writes[0], expected)
	}
	replay, err := a.ApplyGitHubTaskEvent(t.Context(), metadata, payload, false)
	if err != nil || len(replay.Effects) != 1 || replay.Effects[0].Applied || len(fake.writes) != 1 {
		t.Fatalf("replay = %+v, writes = %v, err = %v", replay, fake.writes, err)
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
	if _, err = a.ApplyGitHubTaskEvent(t.Context(), metadata, path, false); err == nil || !strings.Contains(err.Error(), "approved nscaledev/devflow") || len(f.writes) > 0 {
		t.Fatalf("consumer apply: writes=%v, err=%v", f.writes, err)
	}
}
