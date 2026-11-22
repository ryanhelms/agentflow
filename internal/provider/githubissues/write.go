package githubissues

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
)

func taskResult(task provider.Task, status provider.WriteStatus) provider.WriteResult {
	return provider.WriteResult{Ref: task.Ref, URL: task.URL, Status: status}
}

// Create supports a title, Markdown description and labels. A stable operation
// ID lets replays find an issue whose body still contains the operation marker.
func (a *Adapter) Create(ctx context.Context, draft provider.TaskDraft, options provider.WriteOptions) (provider.WriteResult, error) {
	return a.create(ctx, draft, options, nil)
}

// ErrWriteOutcomeUnknown means an issue may exist even though its operation
// marker could not be confirmed. Retain any durable claim on this error.
var ErrWriteOutcomeUnknown = errors.New("write outcome unknown")

// CreateOnce reconciles an existing operation before calling claim. The caller
// must record the claim durably and return false when another attempt owns it.
// A previously claimed operation whose marker is still missing is read-only.
func (a *Adapter) CreateOnce(ctx context.Context, draft provider.TaskDraft, options provider.WriteOptions, claim func() (bool, error)) (provider.WriteResult, error) {
	if options.OperationID == "" || claim == nil {
		return provider.WriteResult{}, errors.New("github issue create-once requires an operation id and claim")
	}
	return a.create(ctx, draft, options, claim)
}

func (a *Adapter) create(ctx context.Context, draft provider.TaskDraft, options provider.WriteOptions, claim func() (bool, error)) (provider.WriteResult, error) {
	if options.ExpectedRevision != "" {
		return provider.WriteResult{}, provider.Unsupported(a.instance, "github issue revision preconditions")
	}
	if err := ValidateDraft(a.instance, draft); err != nil {
		return provider.WriteResult{}, err
	}
	repo, err := a.scope(draft.Scope)
	if err != nil {
		return provider.WriteResult{}, err
	}
	path := issuesPath(repo)
	marker := a.bodyMarker(path, options.OperationID, draft.Template)
	operationKey := a.operationKey(path, options.OperationID)
	lookup := func() (provider.WriteResult, error) {
		var result provider.WriteResult
		err := listPages(ctx, a, path, issueListParams(), func(i issue) (bool, error) {
			if i.PullRequest != nil {
				return false, nil
			}
			matched := hasMarker(i.Body, marker)
			if claim != nil {
				matched = hasOperationMarker(i.Body, operationKey)
			}
			if !matched {
				return false, nil
			}
			task, err := a.task(repo, i)
			if err != nil {
				return false, err
			}
			result = taskResult(task, provider.AlreadyApplied)
			return true, nil
		})
		return result, err
	}
	post := func() (provider.WriteResult, error) {
		in := struct {
			Title  string   `json:"title"`
			Body   string   `json:"body"`
			Labels []string `json:"labels,omitempty"`
		}{draft.Title, markedBody(draft.Description, marker), draft.Labels}
		var i issue
		if err := a.do(ctx, http.MethodPost, path, in, &i); err != nil {
			return provider.WriteResult{}, err
		}
		task, err := a.task(repo, i)
		if err != nil {
			return provider.WriteResult{}, err
		}
		if draft.Template != nil && !hasMarker(i.Body, marker) {
			return provider.WriteResult{}, errors.New("github did not confirm the selected issue template")
		}
		if claim != nil && !hasOperationMarker(i.Body, operationKey) {
			return provider.WriteResult{}, errors.New("github did not confirm the issue operation marker")
		}
		return taskResult(task, provider.Applied), nil
	}
	result, err := postOnce(path, options.OperationID != "", lookup, claim, post)
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("create github issue in %s: %w", repo, err)
	}
	return result, nil
}

func (a *Adapter) Transition(ctx context.Context, ref provider.Ref, target provider.TaskState, options provider.WriteOptions) (provider.WriteResult, error) {
	if options.ExpectedRevision != "" {
		return provider.WriteResult{}, provider.Unsupported(a.instance, "github issue revision preconditions")
	}
	id := target.ID
	if id == "" {
		id = target.Name
	}
	desired, err := taskState(strings.ToLower(strings.TrimSpace(id)))
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("%w: %w", provider.ErrUnsupported, err)
	}
	task, err := a.Get(ctx, ref)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if err := checkTask(task, options); err != nil {
		return provider.WriteResult{}, err
	}
	if task.State == desired {
		return taskResult(task, provider.AlreadyApplied), nil
	}
	var updated issue
	if err := a.do(ctx, http.MethodPatch, issuePath(task.Ref), map[string]string{"state": desired.ID}, &updated); err != nil {
		return provider.WriteResult{}, err
	}
	result, err := a.task(task.Ref.Scope, updated)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if result.Ref != task.Ref || result.State != desired {
		return provider.WriteResult{}, fmt.Errorf("github did not confirm transition of %s to %s", task.Key, desired.ID)
	}
	return taskResult(result, provider.Applied), nil
}

type comment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

// Comment supports the same operation marker and replay behavior as Create.
func (a *Adapter) Comment(ctx context.Context, ref provider.Ref, body string, options provider.WriteOptions) (provider.WriteResult, error) {
	if options.ExpectedRevision != "" {
		return provider.WriteResult{}, provider.Unsupported(a.instance, "github issue revision preconditions")
	}
	if strings.TrimSpace(body) == "" {
		return provider.WriteResult{}, errors.New("github issue comment body is required")
	}
	task, err := a.Get(ctx, ref)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if err := checkTask(task, options); err != nil {
		return provider.WriteResult{}, err
	}
	path := issuePath(task.Ref) + "/comments"
	marker := a.bodyMarker(path, options.OperationID, nil)
	lookup := func() (provider.WriteResult, error) {
		var result provider.WriteResult
		err := listPages(ctx, a, path, url.Values{}, func(c comment) (bool, error) {
			if c.ID <= 0 || !hasMarker(c.Body, marker) {
				return false, nil
			}
			result = provider.WriteResult{Ref: task.Ref, Status: provider.AlreadyApplied, URL: c.HTMLURL}
			return true, nil
		})
		return result, err
	}
	post := func() (provider.WriteResult, error) {
		var c comment
		if err := a.do(ctx, http.MethodPost, path, map[string]string{"body": markedBody(body, marker)}, &c); err != nil {
			return provider.WriteResult{}, err
		}
		if c.ID <= 0 {
			return provider.WriteResult{}, errors.New("github comment response has no comment id")
		}
		return provider.WriteResult{Ref: task.Ref, Status: provider.Applied, URL: c.HTMLURL}, nil
	}
	result, err := postOnce(path, options.OperationID != "", lookup, nil, post)
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("comment on github issue %s: %w", task.Key, err)
	}
	return result, nil
}

// AssignToMe adds the authenticated user without removing other assignees.
func (a *Adapter) AssignToMe(ctx context.Context, ref provider.Ref, options provider.WriteOptions) (provider.WriteResult, error) {
	if options.ExpectedRevision != "" {
		return provider.WriteResult{}, provider.Unsupported(a.instance, "github issue revision preconditions")
	}
	canonical, err := a.issueRef(ref)
	if err != nil {
		return provider.WriteResult{}, err
	}
	i, task, err := a.readIssue(ctx, canonical)
	if err != nil {
		return provider.WriteResult{}, err
	}
	login, err := a.viewer(ctx)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if i.assignedTo(login) {
		return taskResult(task, provider.AlreadyApplied), nil
	}
	var updated issue
	in := map[string][]string{"assignees": {login}}
	if err := a.do(ctx, http.MethodPost, issuePath(task.Ref)+"/assignees", in, &updated); err != nil {
		return provider.WriteResult{}, err
	}
	result, err := a.task(task.Ref.Scope, updated)
	if err != nil {
		return provider.WriteResult{}, err
	}
	if result.Ref != task.Ref || !updated.assignedTo(login) {
		return provider.WriteResult{}, fmt.Errorf("github did not confirm assignment of %s to %s", task.Key, login)
	}
	return taskResult(result, provider.Applied), nil
}

const (
	markerPrefix = "<!-- devflow:github-issues:operation:v1:"
	markerSuffix = " -->"
	markerGap    = "\n\n"
	bodyEscape   = "<!-- devflow:github-issues:body:v1 -->\n\n"
)

func (a *Adapter) bodyMarker(path, id string, template *provider.TaskTemplateRef) string {
	key := a.operationKey(path, id)
	if template != nil {
		return templateMarker(*template, key)
	}
	if key == "" {
		return ""
	}
	return markerPrefix + key + markerSuffix
}

func (a *Adapter) operationKey(path, id string) string {
	if id == "" {
		return ""
	}
	return (provider.Ref{Instance: a.instance, Scope: path, ID: id}).StorageKey()
}

func hasOperationMarker(body, key string) bool {
	if hasMarker(body, markerPrefix+key+markerSuffix) {
		return true
	}
	matches := templateHeader.FindStringSubmatch(body)
	return matches != nil && matches[3] == key
}

func markedBody(body, marker string) string {
	if marker != "" {
		if body == "" {
			return marker
		}
		return marker + markerGap + body
	}
	if plainBody(body) != body || strings.HasPrefix(body, templatePrefix) {
		return bodyEscape + body
	}
	return body
}

func hasMarker(body, marker string) bool {
	return body == marker || strings.HasPrefix(body, marker+markerGap)
}

// Only the leading envelope belongs to the adapter. The remaining bytes are
// the user's Markdown, including whitespace and any marker-like content.
func plainBody(body string) string {
	if rest, ok := strings.CutPrefix(body, bodyEscape); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(body, markerPrefix); ok {
		key, after, found := strings.Cut(rest, markerSuffix)
		if found && len(key) == 64 {
			if _, err := hex.DecodeString(key); err == nil {
				if after == "" {
					return ""
				}
				if content, ok := strings.CutPrefix(after, markerGap); ok {
					return content
				}
			}
		}
	}
	return body
}

// postOnce checks for the marker before posting and after an ambiguous reply.
// GitHub provides no atomic check-and-create here. Concurrent calls with the
// same ID, delayed visibility, and marker removal can allow duplicates. Callers
// should serialize attempts for a given ID. Each invocation sends at most one POST.
func postOnce(path string, reconcile bool, lookup func() (provider.WriteResult, error), claim func() (bool, error), post func() (provider.WriteResult, error)) (provider.WriteResult, error) {
	if reconcile {
		if found, err := lookup(); err != nil || found.Status != "" {
			return found, err
		}
	}
	if claim != nil {
		owned, err := claim()
		if err != nil {
			return provider.WriteResult{}, err
		}
		if !owned {
			return provider.WriteResult{}, fmt.Errorf("%w: earlier attempt has no visible operation marker", ErrWriteOutcomeUnknown)
		}
	}
	result, err := post()
	if err == nil {
		return result, nil
	}
	status := httpStatus(err, http.MethodPost, path)
	if errors.Is(err, provider.ErrNotFound) || (status >= 400 && status < 500 && status != http.StatusRequestTimeout) {
		return provider.WriteResult{}, err
	}
	if reconcile {
		found, lookupErr := lookup()
		if lookupErr != nil {
			return provider.WriteResult{}, fmt.Errorf("%w: %w", ErrWriteOutcomeUnknown, errors.Join(err, fmt.Errorf("check operation marker: %w", lookupErr)))
		}
		if found.Status != "" {
			return found, nil
		}
	}
	return provider.WriteResult{}, fmt.Errorf("%w: %w", ErrWriteOutcomeUnknown, err)
}
