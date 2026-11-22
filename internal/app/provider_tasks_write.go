package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nscaledev/devflow/internal/config"

	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/asana"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/notifications"
)

type taskWritePreview struct {
	Binding native.WriteBinding `json:"binding"`
	Call    native.Call         `json:"call"`
}

type boundTaskWrite struct {
	taskWritePreview
	mapped nativemap.TaskWrite
}

func bindTaskWrite(ctx context.Context, mapped nativemap.TaskWrite, err error) (*boundTaskWrite, error) {
	if errors.Is(err, provider.ErrUnsupported) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	call := mapped.Call()
	binding, err := native.PrepareWrite(ctx, call)
	if errors.Is(err, native.ErrUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &boundTaskWrite{taskWritePreview{binding, call}, mapped}, nil
}

func (w *boundTaskWrite) apply(ctx context.Context) (provider.WriteResult, error) {
	raw, err := native.Write(ctx, w.Binding, w.Call)
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("native task write: %w", err)
	}
	payload, err := native.Payload(raw)
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("%w: %w", native.ErrWriteUncertain, err)
	}
	result, err := w.mapped.Decode(payload)
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("%w: %w", native.ErrWriteUncertain, err)
	}
	return result, nil
}

// applyTaskAppend makes the single-use approval survive process restarts.
// Keep an uncertain attempt claimed, since a prior append may have succeeded.
func (a *App) applyTaskAppend(ctx context.Context, token string, write *boundTaskWrite, apply func() (provider.WriteResult, error)) (provider.WriteResult, error) {
	if token == "" {
		return provider.WriteResult{}, policy.ErrToken
	}
	encoded, err := json.Marshal(write.taskWritePreview)
	if err != nil {
		return provider.WriteResult{}, err
	}
	store := notifications.FileStore{Dir: filepath.Join(a.Home, "task-write-receipts")}
	key := notifications.ReceiptKey{
		OperationID: fmt.Sprintf("%x", sha256.Sum256([]byte(token))),
		Destination: write.Call.Provider,
	}
	state, err := store.Claim(ctx, key, sha256.Sum256(encoded))
	if err != nil {
		return provider.WriteResult{}, fmt.Errorf("claim native task append: %w", err)
	}
	if state != notifications.Claimed {
		return provider.WriteResult{}, policy.ErrToken
	}
	result, err := apply()
	if err != nil {
		return result, err
	}
	if err := store.Finish(context.WithoutCancel(ctx), key, notifications.Delivered); err != nil {
		return provider.WriteResult{}, fmt.Errorf("%w: record native task append: %w", native.ErrWriteUncertain, err)
	}
	return result, nil
}

func readBoundTaskData[T any](ctx context.Context, w *boundTaskWrite, read nativemap.Read[T]) (T, error) {
	var zero T
	call := read.Call()
	var args map[string]any
	if err := json.Unmarshal(call.Arguments(), &args); err != nil {
		return zero, err
	}
	raw, err := native.ReadBound(ctx, w.Binding, native.Call{
		Provider: string(call.Capability().Kind), Server: w.Call.Server, Tool: call.Capability().Tool, Arguments: args,
	})
	if err != nil {
		return zero, err
	}
	payload, err := native.Payload(raw)
	if err != nil {
		return zero, err
	}
	return read.Decode(payload)
}

func (w *boundTaskWrite) task(ctx context.Context, ref provider.Ref) (provider.Task, error) {
	var read nativemap.Read[provider.Task]
	var err error
	switch ref.Instance {
	case "asana":
		read, err = nativemap.AsanaTask(ref)
	case issuesInstance:
		read, err = nativemap.GitHubIssue(ref)
	default:
		err = provider.Unsupported(ref.Instance, "bound task read")
	}
	if err != nil {
		return provider.Task{}, err
	}
	return readBoundTaskData(ctx, w, read)
}

// Check the resolved destination before native preparation, REST or CLI
// fallback. Explicit operation inputs and template defaults use the same ceiling.
func (a *App) taskWriteScope(ctx context.Context, scope string) error {
	sel := a.Cfg.Providers.TaskManagement
	switch sel.Provider {
	case "asana":
		repo, _ := sel.RepositorySettings("project").(*config.AsanaSettings)
		if repo == nil {
			repo, _ = sel.RepositorySettings("transitions").(*config.AsanaSettings)
		}
		if repo != nil && !repo.HasProject(scope) {
			return fmt.Errorf("asana project %q is outside the repository's configured projects: %w", scope, native.ErrDenied)
		}
	case issuesInstance:
		repo, _ := sel.RepositorySettings("repository").(*config.GitHubIssuesSettings)
		hosting := a.Cfg.Providers.CodeHosting.RepositorySettings("target_remote")
		var ceiling string
		switch {
		case repo != nil && repo.Repository != nil:
			ceiling = *repo.Repository
		case repo != nil || hosting != nil:
			var err error
			ceiling, err = a.detectIssueRepo(ctx, hosting)
			if err != nil {
				return fmt.Errorf("resolve repository write scope: %w", errors.Join(native.ErrDenied, err))
			}
		}
		if ceiling != "" && !strings.EqualFold(ceiling, scope) {
			return fmt.Errorf("github issues target %q differs from repository %q: %w", scope, ceiling, native.ErrDenied)
		}
		if err := sel.CheckTarget("repository", scope); err != nil {
			return fmt.Errorf("github issues target %q: %w", scope, errors.Join(native.ErrDenied, err))
		}
	}
	return nil
}

func (a *App) taskTransitionWrite(ctx context.Context, ref provider.Ref, event, operationID string) (*boundTaskWrite, error) {
	if err := a.taskWriteScope(ctx, ref.Scope); err != nil {
		return nil, err
	}
	if operationID != "" || os.Getenv("GITHUB_ACTIONS") == "true" {
		return nil, nil
	}
	target := a.taskTransitions(ref.Scope)[event]
	switch {
	case a.usesAsana() && target == asana.StateCompleted:
		mapped, err := nativemap.AsanaComplete(ref)
		return bindTaskWrite(ctx, mapped, err)
	case a.usesIssues() && target == "closed" && issueOperation() == "":
		mapped, err := nativemap.GitHubIssueClose(ref)
		return bindTaskWrite(ctx, mapped, err)
	}
	return nil, nil
}

func (a *App) transitionProviderTask(ctx context.Context, key, event, token, operationID string) (any, error) {
	ref, err := a.taskRef(ctx, key)
	if err != nil {
		return nil, err
	}
	target := a.taskTransitions(ref.Scope)[event]
	if target == "" || target == "none" {
		return a.planTaskTransition(ctx, taskKey(ref), event, nil)
	}
	write, err := a.taskTransitionWrite(ctx, ref, event, operationID)
	if err != nil {
		return nil, err
	}
	task, write, err := a.prepareTaskForWrite(ctx, ref, write)
	if err != nil {
		return nil, err
	}
	plan, err := a.planTaskTransition(ctx, taskKey(ref), event, &task)
	plan.operationID = operationID
	if err != nil || plan.To == "" {
		return plan, err
	}
	summary := struct {
		TransitionResult
		TaskRef string            `json:"task_ref"`
		Native  *taskWritePreview `json:"native_write,omitempty"`
	}{TransitionResult: plan, TaskRef: ref.String()}
	if write != nil {
		summary.Native = &write.taskWritePreview
	}
	if p, err := a.gate(policy.Remote, "transition", summary, token, token != "", nil); p != nil || err != nil {
		return p, err
	}
	return a.applyPreparedTaskTransition(ctx, ref, plan, write)
}

func (a *App) prepareTaskForWrite(ctx context.Context, ref provider.Ref, write *boundTaskWrite) (provider.Task, *boundTaskWrite, error) {
	task, err := a.taskForWrite(ctx, ref, write)
	if write != nil && errors.Is(err, native.ErrUnavailable) {
		task, err = a.readTaskStore(ctx, ref)
		return task, nil, err
	}
	return task, write, err
}

// GitHub's native issue projection may omit private template markers. The
// existing authenticated issue adapter keeps those bytes available to the
// template check, while the bound read proves the native target and state.
func (a *App) taskForWrite(ctx context.Context, ref provider.Ref, write *boundTaskWrite) (provider.Task, error) {
	if write == nil {
		return a.readTaskStore(ctx, ref)
	}
	task, err := write.task(ctx, ref)
	if err != nil || ref.Instance != issuesInstance {
		return task, err
	}
	source, err := a.readTaskStore(ctx, ref)
	if err != nil {
		return provider.Task{}, err
	}
	if source.Ref != task.Ref || source.State.ID != task.State.ID {
		return provider.Task{}, fmt.Errorf("github issue changed during write preparation: %w", provider.ErrConflict)
	}
	return source, nil
}

func (a *App) applyPreparedTaskTransition(ctx context.Context, ref provider.Ref, plan TransitionResult, write *boundTaskWrite) (TransitionResult, error) {
	if write == nil {
		return a.ApplyTransition(ctx, plan)
	}
	err := a.applyBoundTaskTransition(ctx, ref, &plan, write)
	a.audit(policy.Remote, "transition", struct {
		Transition TransitionResult `json:"transition"`
		Native     taskWritePreview `json:"native_write"`
	}{plan, write.taskWritePreview}, err)
	return plan, err
}

func (a *App) applyBoundTaskTransition(ctx context.Context, ref provider.Ref, plan *TransitionResult, write *boundTaskWrite) error {
	current, err := a.taskForWrite(ctx, ref, write)
	if err != nil {
		return err
	}
	if current.State.ID == plan.To {
		plan.Note = "already " + plan.To
		return nil
	}
	if current.State.Terminal || current.State.ID != plan.From {
		return fmt.Errorf("task state changed since preview: %w", provider.ErrConflict)
	}
	if err := a.checkTaskTemplate(plan.Template)(current); err != nil {
		return err
	}
	if _, err := write.apply(ctx); err != nil {
		return err
	}
	updated, err := write.task(ctx, ref)
	if err != nil {
		return fmt.Errorf("%w: verify native task transition: %w", native.ErrWriteUncertain, err)
	}
	if updated.State.ID != plan.To {
		return fmt.Errorf("%w: native task transition did not reach the approved state: %w", native.ErrWriteUncertain, provider.ErrConflict)
	}
	plan.Applied = true
	return nil
}

func (a *App) commentBoundTask(ctx context.Context, task provider.Task, body, token string, write *boundTaskWrite) (any, error) {
	ref := task.Ref
	snapshot, err := a.validateTaskTemplate(task)
	if err != nil {
		return nil, err
	}
	summary := map[string]any{"key": taskKey(ref), "task_ref": ref.String(), "comment": body, "native_write": write.taskWritePreview}
	if snapshot != nil {
		summary["template"] = snapshot
	}
	// A comment is an append, so every native dispatch requires a single-use approval.
	if p, err := a.gate(policy.Remote, "comment", summary, token, true, nil); p != nil || err != nil {
		return p, err
	}
	_, err = a.applyTaskAppend(ctx, token, write, func() (provider.WriteResult, error) { return write.apply(ctx) })
	a.audit(policy.Remote, "comment", summary, err)
	return map[string]any{"key": taskKey(ref), "commented": err == nil}, err
}
