// Package event turns lifecycle event payloads into provider transition plans.
package event

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/provider"
)

// GitHubActionsContext binds a payload to trusted Actions metadata and repo
// configuration. EventID identifies the event or workflow run and stays the
// same on retries (for example GITHUB_RUN_ID, without GITHUB_RUN_ATTEMPT).
// DefaultBranch comes from trusted repository metadata and is used for merges.
// TaskInstance and TaskScope identify the configured destination exactly as
// the selected adapter represents it.
type GitHubActionsContext struct {
	Repository    provider.RepositoryRef
	RepositoryID  int64
	DefaultBranch string
	EventName     string
	EventID       string
	TaskInstance  string
	TaskScope     string
	ApprovedIssue string
}

type SkipReason string

const (
	UnsupportedEvent   SkipReason = "unsupported_event"
	UnsupportedAction  SkipReason = "unsupported_action"
	RepositoryMismatch SkipReason = "repository_mismatch"
	OptedOut           SkipReason = "opted_out"
	Draft              SkipReason = "draft"
	Unmerged           SkipReason = "unmerged"
	NonDefaultBranch   SkipReason = "non_default_branch"
	NoTaskRefs         SkipReason = "no_task_refs"
)

// Plan contains transition intents for a single pull request delivery.
// A skipped plan has a reason and no transitions.
type Plan struct {
	Repository   provider.RepositoryRef `json:"repository"`
	RepositoryID int64                  `json:"repository_id"`
	EventName    string                 `json:"event_name"`
	EventID      string                 `json:"event_id"`
	PullRequest  int64                  `json:"pull_request,omitempty"`
	Action       string                 `json:"action,omitempty"`
	Lifecycle    string                 `json:"lifecycle,omitempty"`
	Skip         SkipReason             `json:"skip,omitempty"`
	Transitions  []Transition           `json:"transitions,omitempty"`
}

type Transition struct {
	Task        provider.Ref `json:"task"`
	OperationID string       `json:"operation_id"`
}

type githubRepository struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type githubBranch struct {
	Ref  string           `json:"ref"`
	Repo githubRepository `json:"repo"`
}

type pullRequestEvent struct {
	Action      string           `json:"action"`
	Number      int64            `json:"number"`
	Repository  githubRepository `json:"repository"`
	PullRequest struct {
		Number int64        `json:"number"`
		Body   string       `json:"body"`
		Draft  *bool        `json:"draft"`
		Merged *bool        `json:"merged"`
		Base   githubBranch `json:"base"`
		Head   githubBranch `json:"head"`
	} `json:"pull_request"`
}

var (
	repositoryPath = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)
	skipBody       = regexp.MustCompile(`(?im)\[devflow skip\]|^Devflow:[\t \r\v\f]*skip`)
)

// PlanGitHubActions parses a GitHub Actions payload without reading files,
// consulting providers, or applying transitions. It uses provider.TaskRefs
// for explicit trailers in the PR body.
//
// A later app wrapper can expose
// PlanGitHubActionsEvent(ctx context.Context, eventPath string, actions event.GitHubActionsContext) (event.Plan, error).
// It should read GITHUB_EVENT_PATH, bind Actions metadata to the trusted repo
// and selected task adapter, then pass the payload bytes here. The app owns
// lifecycle-to-state mapping and passes OperationID in provider.WriteOptions
// when applying an intent. The legacy Jira workflow still uses handle-event.sh.
func PlanGitHubActions(actions GitHubActionsContext, payload []byte) (Plan, error) {
	plan := Plan{
		Repository: actions.Repository, RepositoryID: actions.RepositoryID,
		EventName: actions.EventName, EventID: actions.EventID,
	}
	if actions.EventName != "pull_request" && actions.EventName != "pull_request_target" {
		plan.Skip = UnsupportedEvent
		return plan, nil
	}

	var event pullRequestEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return Plan{}, fmt.Errorf("decode github event: %w", err)
	}
	if event.Action == "" {
		return Plan{}, errors.New("github event has no pull request action")
	}
	switch event.Action {
	case "opened", "reopened", "ready_for_review", "closed":
	default:
		plan.Skip = UnsupportedAction
		return plan, nil
	}
	if err := actions.validate(); err != nil {
		return Plan{}, err
	}
	pr := event.PullRequest
	if !actions.matches(event.Repository) || !actions.matches(pr.Base.Repo) || !actions.matches(pr.Head.Repo) {
		plan.Skip = RepositoryMismatch
		return plan, nil
	}
	if event.Number < 1 || pr.Number != event.Number {
		return Plan{}, errors.New("github event requires matching positive pull request numbers")
	}
	plan.Action, plan.PullRequest = event.Action, pr.Number
	lifecycle, skip, err := event.lifecycle(actions.DefaultBranch)
	if err != nil {
		return Plan{}, err
	}
	plan.Lifecycle, plan.Skip = lifecycle, skip
	if skip != "" {
		return plan, nil
	}

	refs, err := provider.TaskRefs(pr.Body)
	if err != nil {
		return Plan{}, fmt.Errorf("parse pull request task trailers: %w", err)
	}
	if len(refs) == 0 {
		plan.Skip = NoTaskRefs
		return plan, nil
	}
	for _, ref := range refs {
		if ref.Instance != actions.TaskInstance || ref.Scope != actions.TaskScope {
			return Plan{}, errors.New("pull request task reference is outside the selected instance or scope")
		}
		plan.Transitions = append(plan.Transitions, Transition{Task: ref, OperationID: plan.operationID(ref)})
	}
	return plan, nil
}

func (actions GitHubActionsContext) validate() error {
	repo := actions.Repository
	if actions.RepositoryID < 1 || strings.TrimSpace(repo.Host) == "" ||
		strings.ContainsFunc(repo.Host, unicode.IsControl) || !repositoryPath.MatchString(repo.Path) ||
		strings.HasSuffix(repo.Path, "/.") || strings.HasSuffix(repo.Path, "/..") {
		return errors.New("github event requires a trusted repository host, owner/name and id")
	}
	if strings.TrimSpace(actions.EventID) == "" || strings.ContainsFunc(actions.EventID, unicode.IsControl) {
		return errors.New("github event requires a trusted event id")
	}
	target := provider.Ref{Instance: actions.TaskInstance, Scope: actions.TaskScope, ID: "task"}
	if err := target.Validate(); err != nil {
		return fmt.Errorf("selected task destination: %w", err)
	}
	return nil
}

func (actions GitHubActionsContext) matches(repo githubRepository) bool {
	return repo.ID == actions.RepositoryID && strings.EqualFold(repo.FullName, actions.Repository.Path)
}

func (e pullRequestEvent) lifecycle(defaultBranch string) (string, SkipReason, error) {
	pr := e.PullRequest
	if skipBody.MatchString(pr.Body) {
		return "", OptedOut, nil
	}
	if e.Action == "closed" {
		switch {
		case pr.Merged == nil:
			return "", "", errors.New("github close event has no pull request merged flag")
		case !*pr.Merged:
			return "", Unmerged, nil
		case defaultBranch == "" || pr.Base.Ref == "":
			return "", "", errors.New("github merge event requires a base branch and a trusted default branch")
		case pr.Base.Ref != defaultBranch:
			return "", NonDefaultBranch, nil
		default:
			return flowmap.PRMerged, "", nil
		}
	}
	if pr.Draft == nil {
		return "", "", errors.New("github review event has no pull request draft flag")
	}
	if *pr.Draft {
		return "", Draft, nil
	}
	return flowmap.PROpened, "", nil
}

func (p Plan) operationID(ref provider.Ref) string {
	fields := []string{
		"github-actions-lifecycle-v1", strings.ToLower(p.Repository.Host), strings.ToLower(p.Repository.Path),
		strconv.FormatInt(p.RepositoryID, 10), p.EventName, p.EventID, p.Action, p.Lifecycle,
		strconv.FormatInt(p.PullRequest, 10), ref.Instance, ref.Scope, ref.ID,
	}
	// Each field is either validated to exclude controls or comes from a
	// constant or number. NUL separates fields even when a ref contains "/".
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(fields, "\x00"))))
}
