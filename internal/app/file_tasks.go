package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/filesystem"
	"github.com/nscaledev/devflow/internal/provider/repository"
	"github.com/nscaledev/devflow/internal/tasktemplate"
)

func (a *App) fileTaskSettings() config.FileTaskSettings {
	switch s := a.Cfg.Providers.TaskManagement.Settings.(type) {
	case *config.FileSystemTaskSettings:
		return s.FileTaskSettings
	default:
		return a.Cfg.Providers.TaskManagement.Settings.(*config.RepositoryTaskSettings).FileTaskSettings
	}
}

func (a *App) fileScope() string {
	if scope := a.fileTaskSettings().Scope; scope != "" {
		return scope
	}
	return "default"
}

type fileTaskTarget struct {
	root string
	repo repository.Config
}

func (a *App) fileTarget(ctx context.Context, write bool) (fileTaskTarget, error) {
	var t fileTaskTarget
	switch s := a.Cfg.Providers.TaskManagement.Settings.(type) {
	case *config.FileSystemTaskSettings:
		if strings.TrimSpace(s.Root) == "" {
			return t, errors.New("task directory is required")
		}
		t.root = filepath.Clean(s.Root)
		if !filepath.IsAbs(t.root) {
			t.root = filepath.Join(a.Repo.Dir, t.root)
		}
	case *config.RepositoryTaskSettings:
		origin, err := a.taskOrigin(ctx, s.Repository, write)
		if err != nil {
			return t, err
		}
		t.repo = repository.Config{Instance: "repository", Origin: origin, Directory: s.Directory, Publication: s.Publication}
		if s.Branch != nil {
			t.repo.Branch = *s.Branch
		}
	default:
		return t, errors.New("expected a file-backed task provider")
	}
	return t, nil
}

// Named remotes are resolved in the caller's checkout. A private adapter
// checkout has no access to those names or checkout-relative paths.
func (a *App) taskOrigin(ctx context.Context, configured *string, write bool) (string, error) {
	origin := "origin"
	if configured != nil && *configured != "" {
		origin = *configured
	}
	remotes, err := a.Repo.Git(ctx, "remote")
	if err != nil {
		return "", err
	}
	if slices.Contains(strings.Split(strings.TrimSuffix(remotes, "\n"), "\n"), origin) {
		raw, err := a.Repo.Git(ctx, "remote", "get-url", "--all", "--", origin)
		if err != nil {
			return "", err
		}
		if write {
			push, err := a.Repo.Git(ctx, "remote", "get-url", "--push", "--all", "--", origin)
			if err != nil {
				return "", err
			}
			if push != raw {
				return "", errors.New("task remote has different fetch and push destinations; configure one repository URL")
			}
		}
		origin = strings.TrimSuffix(raw, "\n")
	} else if configured == nil || *configured == "" {
		return "", errors.New("no origin remote for repository tasks; configure task_management.settings.repository")
	}
	if origin == "" || strings.ContainsAny(origin, "\r\n") {
		return "", errors.New("expected one task repository URL or directory")
	}
	if strings.Contains(origin, "://") {
		u, err := url.Parse(origin)
		if err != nil || u.RawQuery != "" || u.Fragment != "" || (u.User != nil && u.Scheme != "ssh") {
			return "", errors.New("task repository URL must omit credentials, query and fragment")
		}
		if _, ok := u.User.Password(); ok {
			return "", errors.New("task repository URL must omit passwords")
		}
	} else if _, err := gitx.ParseRemoteURL(origin); errors.Is(err, gitx.ErrLocalRemote) && !filepath.IsAbs(origin) {
		origin = filepath.Join(a.Repo.Dir, origin)
	}
	return origin, nil
}

func (t fileTaskTarget) open() (provider.Tasks, func(), error) {
	if t.root != "" {
		s, err := filesystem.NewTasks("filesystem", t.root)
		if err != nil {
			return nil, nil, err
		}
		return s, func() { _ = s.Close() }, nil
	}
	s, err := repository.NewTasks(t.repo)
	return s, func() {}, err
}

type fileTaskMutation func(provider.Tasks) (provider.WriteResult, error)

// The adapter resolves remote HEAD once per write. Gate inside BeforeWrite so
// the branch shown for approval is exactly the branch passed to Git.
func (a *App) writeFileTask(ctx context.Context, action string, summary map[string]any, token string, ref provider.Ref, mutate fileTaskMutation) (provider.WriteResult, *Pending, error) {
	var zero provider.WriteResult
	target, err := a.fileTarget(ctx, true)
	if err != nil {
		return zero, nil, err
	}
	var pending *Pending
	class := policy.Local
	errPending := errors.New("task publication pending approval")
	if target.root != "" {
		summary["directory"] = target.root
		p, err := a.gate(class, action, summary, token, action == "ticket_create", nil)
		if p != nil || err != nil {
			return zero, p, err
		}
		if action == "ticket_create" {
			if err := os.MkdirAll(target.root, 0700); err != nil {
				return zero, nil, err
			}
		}
	} else {
		class = policy.Remote
		if target.repo.Publication != "push" {
			target.repo.Publication = "pull-request"
			target.repo.Publisher, err = a.repositoryPublisher(ctx, target.repo.Origin, "task", ref)
			if err != nil {
				return zero, nil, err
			}
		}
		summary["origin"], summary["directory"], summary["publication"] = target.repo.Origin, target.repo.Directory, target.repo.Publication
		target.repo.BeforeWrite = func(base, branch string) error {
			if policy.IsProtected(branch, a.Cfg.Policy.ProtectedBranches) {
				return fmt.Errorf("pushing to protected branch %q is forbidden", branch)
			}
			summary["base"], summary["target"] = base, branch
			var gateErr error
			pending, gateErr = a.gate(class, action, summary, token, true, nil)
			if pending != nil {
				return errPending
			}
			return gateErr
		}
	}
	tasks, closeStore, err := target.open()
	var result provider.WriteResult
	if err == nil {
		defer closeStore()
		result, err = mutate(tasks)
	}
	if errors.Is(err, errPending) {
		return zero, pending, nil
	}
	a.audit(class, action, summary, err)
	return result, nil, err
}

func (a *App) createFileTask(ctx context.Context, scope, typ, title, body, linkTo string, template tasktemplate.Ref, token string) (any, error) {
	instance := a.Cfg.Providers.TaskManagement.Provider
	draft := provider.TaskDraft{Scope: scope, Title: title, Description: body, Type: typ}
	var link provider.Ref
	var err error
	if linkTo != "" {
		link, err = a.taskRef(ctx, linkTo)
	} else {
		link, err = a.branchTask(ctx)
	}
	if err != nil {
		return nil, err
	}
	if link.ID != "" {
		draft.Links = []provider.Link{{Ref: &link}}
	}
	draft, err = a.resolveFileTaskDraft(draft, template)
	if err != nil {
		return nil, err
	}
	// Omit a missing pin to preserve the approval payload and operation IDs
	// for tasks created before TaskDraft carried a template.
	approvedDraft := struct {
		Template *provider.TaskTemplateRef `json:",omitempty"`
		provider.TaskDraft
	}{draft.Template, draft}
	operationID := issueOperation()
	repo, repositoryTask := a.Cfg.Providers.TaskManagement.Settings.(*config.RepositoryTaskSettings)
	interactiveReview := repositoryTask && repo.Publication != "push" && operationID == ""
	// Repository PRs name their review branch before the complete destination
	// is known. All agents preparing this draft share that branch until the
	// receipt closes, even when a different process executes the approved write.
	proposal := map[string]any{"provider": instance, "task": approvedDraft, "repository": repo}
	if interactiveReview {
		operationID, err = a.Gate.ProposalNonce(policy.Remote, "ticket_create", proposal, token)
		if err != nil {
			return nil, err
		}
	}
	summary := map[string]any{"provider": instance, "task": approvedDraft, "operation_id": operationID}
	opts, err := issueWriteOptions("ticket_create", summary, token)
	if err != nil {
		return nil, err
	}
	w, p, err := a.writeFileTask(ctx, "ticket_create", summary, token, provider.Ref{Instance: instance, Scope: draft.Scope}, func(tasks provider.Tasks) (provider.WriteResult, error) {
		return tasks.Create(ctx, draft, opts)
	})
	if p != nil || err != nil {
		if interactiveReview && p != nil && p.Approval != nil {
			if bindErr := a.Gate.BindProposal(policy.Remote, "ticket_create", proposal, p.Approval.OperationID, operationID); bindErr != nil {
				return nil, bindErr
			}
		}
		return p, err
	}
	if w.Status == provider.PendingReview {
		return map[string]string{"key": w.Ref.String(), "status": string(w.Status), "url": w.URL, "branch": w.Branch}, nil
	}
	return map[string]string{"key": w.Ref.String()}, nil
}

func (a *App) transitionFileTask(ctx context.Context, plan TransitionResult, token string) (any, error) {
	ref, err := a.taskRef(ctx, plan.Key)
	if err != nil {
		return nil, err
	}
	summary := map[string]any{"task_ref": ref.String(), "transition": plan, "revision": plan.revision}
	opts := provider.WriteOptions{
		ExpectedRevision: plan.revision, OperationID: ref.String() + "/" + plan.Event + "/" + plan.revision,
		CheckTask: a.checkTaskTemplate(plan.Template),
	}
	result, p, err := a.writeFileTask(ctx, "transition", summary, token, ref, func(tasks provider.Tasks) (provider.WriteResult, error) {
		return tasks.Transition(ctx, ref, provider.TaskState{ID: "closed", Name: "Closed", Terminal: true}, opts)
	})
	if p != nil || err != nil {
		return p, err
	}
	plan.Applied = result.Status == provider.Applied
	if result.Status == provider.PendingReview {
		plan.Status, plan.URL, plan.Branch = result.Status, result.URL, result.Branch
	}
	if result.Status == provider.AlreadyApplied {
		plan.Note = "already closed"
	}
	return plan, nil
}
