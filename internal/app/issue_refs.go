package app

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/githubissues"
)

const issuesInstance = "github-issues"

func (a *App) usesIssues() bool { return a.Cfg.Providers.TaskManagement.Provider == issuesInstance }

func (a *App) issueSettings() *config.GitHubIssuesSettings {
	return a.Cfg.Providers.TaskManagement.Settings.(*config.GitHubIssuesSettings)
}

func (a *App) issueTasks(ctx context.Context) (provider.Tasks, error) {
	if a.tasks != nil {
		return a.tasks, nil
	}
	c, err := a.GitHub(ctx)
	if err != nil {
		return nil, err
	}
	// Each operation passes a fully resolved scope to the adapter.
	return githubissues.New(c, issuesInstance, "")
}

var repoPattern = regexp.MustCompile(`^[a-zA-Z0-9-]+/[a-zA-Z0-9_.-]+$`)

func issueScope(scope string) (string, error) {
	if !repoPattern.MatchString(scope) || strings.HasSuffix(scope, "/.") || strings.HasSuffix(scope, "/..") {
		return "", fmt.Errorf("github issues requires an owner/repo repository, got %q", scope)
	}
	return strings.ToLower(scope), nil
}

func (a *App) defaultIssueRepo(ctx context.Context) (string, error) {
	if configured := a.issueSettings().Repository; configured != nil {
		return issueScope(*configured)
	}
	opts := gitx.DetectOptions{}
	if cfg, ok := a.Cfg.Providers.CodeHosting.Settings.(*config.CodeHostingSettings); ok {
		opts.Hosts = cfg.Hosts
		if cfg.SourceRemote != nil {
			opts.Source = *cfg.SourceRemote
		}
		if cfg.TargetRemote != nil {
			opts.Target = *cfg.TargetRemote
		}
	}
	d, err := a.Repo.DetectForge(ctx, opts)
	if err != nil {
		return "", err
	}
	// The adapter's authenticated REST client uses api.github.com.
	if !d.OK() || d.Forge != gitx.ForgeGitHub || !strings.HasPrefix(d.Target.ID(), "github.com/") {
		return "", errors.New("no known GitHub issue repository; configure task_management.settings.repository or specify owner/repo")
	}
	return issueScope(d.Target.URL.Path)
}

func validatedIssueRef(ref provider.Ref) (provider.Ref, error) {
	if ref.Instance != issuesInstance {
		return provider.Ref{}, fmt.Errorf("expected github-issues reference, got %q", ref.Instance)
	}
	scope, err := issueScope(ref.Scope)
	if err != nil {
		return provider.Ref{}, err
	}
	n, err := strconv.ParseInt(ref.ID, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != ref.ID {
		return provider.Ref{}, fmt.Errorf("invalid github issue number %q", ref.ID)
	}
	ref.Scope = scope
	return ref, nil
}

func taskKey(ref provider.Ref) string {
	if ref.Instance == issuesInstance {
		return ref.Scope + "#" + ref.ID
	}
	return ref.String()
}

func (a *App) validateTaskRef(ref provider.Ref) (provider.Ref, error) {
	if a.usesIssues() {
		return validatedIssueRef(ref)
	}
	if ref.Instance != a.Cfg.Providers.TaskManagement.Provider {
		return provider.Ref{}, fmt.Errorf("expected %s task reference, got %q", a.Cfg.Providers.TaskManagement.Provider, ref.Instance)
	}
	return ref, ref.Validate()
}

func (a *App) taskRef(ctx context.Context, input string) (provider.Ref, error) {
	if a.usesIssues() {
		return a.issueRef(ctx, input)
	}
	if input == "" {
		ref, err := a.branchTask(ctx)
		if err == nil && ref.ID == "" {
			err = errors.New("pass a qualified task reference or add a Task-Ref trailer to a branch commit")
		}
		return ref, err
	}
	ref, err := provider.ParseRef(input)
	if err != nil {
		return ref, err
	}
	return a.validateTaskRef(ref)
}

func (a *App) issueRef(ctx context.Context, input string) (provider.Ref, error) {
	if input == "" {
		ref, err := a.branchTask(ctx)
		if err == nil && ref.ID == "" {
			err = errors.New("no issue bound to this branch; pass owner/repo#number or run devflow start")
		}
		return ref, err
	}
	scope, id, ok := strings.Cut(input, "#")
	if !ok {
		ref, err := provider.ParseRef(input)
		if err != nil {
			return provider.Ref{}, fmt.Errorf("expected owner/repo#number or a qualified task reference: %w", err)
		}
		return validatedIssueRef(ref)
	}
	if scope == "" {
		var err error
		scope, err = a.defaultIssueRepo(ctx)
		if err != nil {
			return provider.Ref{}, err
		}
	}
	return validatedIssueRef(provider.Ref{Instance: issuesInstance, Scope: scope, ID: id})
}

// Branch configuration is local, shared by linked worktrees, and follows a
// git branch rename. Read only repository-local configuration for a binding.
func (a *App) boundTask(ctx context.Context, branch string) (provider.Ref, error) {
	raw, err := a.Repo.Git(ctx, "config", "--local", "--get-all", "branch."+branch+".devflowTaskRef")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return provider.Ref{}, nil
		}
		return provider.Ref{}, err
	}
	ref, err := provider.ParseRef(strings.TrimSuffix(raw, "\n"))
	if err != nil {
		return provider.Ref{}, fmt.Errorf("invalid binding for branch %q: %w", branch, err)
	}
	return a.validateTaskRef(ref)
}

func (a *App) taskKeys(body string) ([]string, error) {
	refs, err := provider.TaskRefs(body)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, ref := range refs {
		ref, err = a.validateTaskRef(ref)
		if err != nil {
			return nil, err
		}
		if key := taskKey(ref); !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (a *App) branchTask(ctx context.Context) (provider.Ref, error) {
	branch := a.Repo.Branch(ctx)
	if branch == "" || branch == a.Repo.DefaultBranch(ctx) || policy.IsProtected(branch, a.Cfg.Policy.ProtectedBranches) {
		return provider.Ref{}, nil
	}
	ref, err := a.boundTask(ctx, branch)
	if err != nil || ref.ID != "" {
		return ref, err
	}
	// A fresh clone has no binding. Only commits unique to this feature
	// branch can supply an identity, and they must agree on one issue.
	keys, err := a.Keys(ctx, a.Repo.BaseRef(ctx)+"..HEAD")
	if err != nil || len(keys) == 0 {
		return provider.Ref{}, err
	}
	if len(keys) > 1 {
		return provider.Ref{}, errors.New("branch commits reference multiple issues; pass an explicit task")
	}
	return a.taskRef(ctx, keys[0])
}
