package event

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/flowmap"
	"github.com/ryanhelms/agentflow/internal/provider"
)

var githubContext = GitHubActionsContext{
	Repository: provider.RepositoryRef{Host: "github.com", Path: "acme/widget"}, RepositoryID: 100,
	DefaultBranch: "main", EventName: "pull_request", EventID: "123456",
	TaskInstance: "issues", TaskScope: "acme/widget",
}

var issue42 = provider.Ref{Instance: "issues", Scope: "acme/widget", ID: "42"}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("testdata", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestGitHubActionsFixtures(t *testing.T) {
	for _, tt := range []struct {
		fixture   string
		lifecycle string
		skip      SkipReason
	}{
		{"pr-opened", flowmap.PROpened, ""},
		{"pr-reopened", flowmap.PROpened, ""},
		{"pr-ready", flowmap.PROpened, ""},
		{"pr-merged", flowmap.PRMerged, ""},
		{"pr-draft", "", Draft},
		{"pr-closed", "", Unmerged},
		{"pr-merged-other-branch", "", NonDefaultBranch},
		{"pr-skipped-marker", "", OptedOut},
		{"pr-skipped-trailer", "", OptedOut},
		{"pr-no-task-ref", flowmap.PROpened, NoTaskRefs},
		{"pr-fork", "", RepositoryMismatch},
	} {
		t.Run(tt.fixture, func(t *testing.T) {
			plan, err := PlanGitHubActions(githubContext, fixture(t, tt.fixture))
			if err != nil || plan.Skip != tt.skip || plan.Lifecycle != tt.lifecycle {
				t.Fatalf("plan = %+v, %v", plan, err)
			}
			if tt.skip != "" {
				if len(plan.Transitions) != 0 {
					t.Fatalf("skipped event produced transitions: %+v", plan)
				}
				return
			}
			if plan.PullRequest != 57 || plan.EventID != githubContext.EventID || plan.Repository != githubContext.Repository ||
				plan.RepositoryID != githubContext.RepositoryID || len(plan.Transitions) != 1 ||
				plan.Transitions[0].Task != issue42 || plan.Transitions[0].OperationID == "" {
				t.Fatalf("unexpected transition plan: %+v", plan)
			}
		})
	}
}

func TestGitHubActionsRepositoryBinding(t *testing.T) {
	payload := fixture(t, "pr-opened")
	base := []byte("\"ref\": \"main\",\n      \"repo\": {\n        \"id\": 100")
	head := []byte("\"ref\": \"work/OPS-3\",\n      \"repo\": {\n        \"id\": 100")
	for _, tt := range []struct {
		name    string
		payload []byte
	}{
		{"fork on pull_request_target", fixture(t, "pr-fork")},
		{"event repository", bytes.Replace(payload, []byte("acme/widget"), []byte("bad/widget"), 1)},
		{"base repository", bytes.Replace(payload, base, []byte(strings.Replace(string(base), "100", "200", 1)), 1)},
		{"missing head repository id", bytes.Replace(payload, head, []byte(strings.Replace(string(head), "100", "null", 1)), 1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			actions := githubContext
			actions.EventName = "pull_request_target"
			plan, err := PlanGitHubActions(actions, tt.payload)
			if err != nil || plan.Skip != RepositoryMismatch || len(plan.Transitions) != 0 {
				t.Fatalf("plan = %+v, %v", plan, err)
			}
		})
	}
}

func TestGitHubActionsTrustedDefaultBranch(t *testing.T) {
	payload := fixture(t, "pr-merged-other-branch")
	payload = bytes.Replace(payload, []byte(`"default_branch": "main"`), []byte(`"default_branch": "release"`), 1)
	plan, err := PlanGitHubActions(githubContext, payload)
	if err != nil || plan.Skip != NonDefaultBranch || len(plan.Transitions) != 0 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
}

func TestGitHubActionsTaskTrailers(t *testing.T) {
	payload := fixture(t, "pr-opened")
	trailer := []byte(provider.TaskTrailer + issue42.String())
	for _, tt := range []struct {
		name     string
		appended string
		want     []provider.Ref
	}{
		{"duplicate and distinct explicit refs", `\r\nTask-Ref: issues/acme%2fwidget/42\nTask-Ref: issues/acme%2Fwidget/43`,
			[]provider.Ref{issue42, {Instance: "issues", Scope: "acme/widget", ID: "43"}}},
		{"references in prose", `\nSee Task-Ref: issues/acme%2Fwidget/43 and #44.`, []provider.Ref{issue42}},
		{"foreign instance", `\nTask-Ref: other/acme%2Fwidget/43`, nil},
		{"foreign scope", `\nTask-Ref: issues/acme%2Fother/43`, nil},
		{"abbreviated reference", `\nTask-Ref: #43`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := provider.TaskTrailer + issue42.String() + tt.appended
			plan, err := PlanGitHubActions(githubContext, bytes.Replace(payload, trailer, []byte(body), 1))
			if tt.want == nil {
				if err == nil || len(plan.Transitions) != 0 {
					t.Fatalf("expected rejection of entire event, got %+v, %v", plan, err)
				}
				return
			}
			var got []provider.Ref
			for _, transition := range plan.Transitions {
				got = append(got, transition.Task)
			}
			if err != nil || !slices.Equal(got, tt.want) {
				t.Fatalf("task refs = %v, %v, want %v", got, err, tt.want)
			}
		})
	}
}

func TestGitHubActionsReplay(t *testing.T) {
	payload := fixture(t, "pr-opened")
	first, err := PlanGitHubActions(githubContext, payload)
	if err != nil || len(first.Transitions) != 1 {
		t.Fatalf("plan = %+v, %v", first, err)
	}
	changedText := bytes.ReplaceAll(payload, []byte("PLAT-12"), []byte("OPS-8"))
	replay, err := PlanGitHubActions(githubContext, changedText)
	if err != nil || !reflect.DeepEqual(replay, first) {
		t.Fatalf("replay = %+v, %v, want %+v", replay, err, first)
	}

	for _, tt := range []struct {
		name     string
		change   func(*GitHubActionsContext)
		old, new string
	}{
		{name: "delivery", change: func(ctx *GitHubActionsContext) { ctx.EventID = "123457" }},
		{name: "trigger", change: func(ctx *GitHubActionsContext) { ctx.EventName = "pull_request_target" }},
		{name: "host", change: func(ctx *GitHubActionsContext) { ctx.Repository.Host = "git.acme.example" }},
		{name: "repository id", change: func(ctx *GitHubActionsContext) { ctx.RepositoryID = 200 }, old: `"id": 100`, new: `"id": 200`},
		{name: "task instance", change: func(ctx *GitHubActionsContext) { ctx.TaskInstance = "tracker" }, old: "issues/acme", new: "tracker/acme"},
		{name: "task scope", change: func(ctx *GitHubActionsContext) { ctx.TaskScope = "acme/other" }, old: "%2Fwidget/42", new: "%2Fother/42"},
		{name: "task id", old: "%2Fwidget/42", new: "%2Fwidget/43"},
		{name: "pull request", old: `"number": 57`, new: `"number": 58`},
		{name: "action", old: `"opened"`, new: `"reopened"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			actions, body := githubContext, payload
			if tt.change != nil {
				tt.change(&actions)
			}
			if tt.old != "" {
				body = bytes.ReplaceAll(payload, []byte(tt.old), []byte(tt.new))
			}
			plan, err := PlanGitHubActions(actions, body)
			if err != nil || len(plan.Transitions) != 1 || plan.Transitions[0].OperationID == first.Transitions[0].OperationID {
				t.Fatalf("changed event = %+v, %v, original %+v", plan, err, first)
			}
		})
	}
}

func TestGitHubActionsRejectsMissingMetadata(t *testing.T) {
	payload := fixture(t, "pr-opened")
	for _, tt := range []struct {
		name    string
		change  func(*GitHubActionsContext)
		payload []byte
	}{
		{name: "malformed JSON", payload: []byte("{")},
		{name: "missing action", payload: []byte("{}")},
		{name: "missing draft flag", payload: bytes.Replace(payload, []byte(`"draft": false`), []byte(`"draft": null`), 1)},
		{name: "inconsistent number", payload: bytes.Replace(payload, []byte(`"number": 57`), []byte(`"number": 58`), 1)},
		{name: "no trusted identity", change: func(ctx *GitHubActionsContext) { ctx.EventID = "" }, payload: payload},
		{name: "no trusted scope", change: func(ctx *GitHubActionsContext) { ctx.TaskScope = "" }, payload: payload},
		{name: "no trusted repository", change: func(ctx *GitHubActionsContext) { ctx.RepositoryID = 0 }, payload: payload},
		{name: "no trusted default branch", change: func(ctx *GitHubActionsContext) { ctx.DefaultBranch = "" }, payload: fixture(t, "pr-merged")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			actions := githubContext
			if tt.change != nil {
				tt.change(&actions)
			}
			if plan, err := PlanGitHubActions(actions, tt.payload); err == nil || len(plan.Transitions) > 0 {
				t.Fatalf("invalid event = %+v, %v", plan, err)
			}
		})
	}
}

func TestGitHubActionsIgnoresOtherEventsAndActions(t *testing.T) {
	for _, tt := range []struct {
		name, payload string
		want          SkipReason
	}{
		{"schedule", "{}", UnsupportedEvent},

		{"pull_request", `{"action":"edited"}`, UnsupportedAction},
		{"pull_request", `{"action":"synchronize"}`, UnsupportedAction},
	} {
		ctx := githubContext
		ctx.EventName = tt.name
		if plan, err := PlanGitHubActions(ctx, []byte(tt.payload)); err != nil || plan.Skip != tt.want || len(plan.Transitions) > 0 {
			t.Errorf("event %q %q = %+v, %v", tt.name, tt.payload, plan, err)
		}
	}
}
