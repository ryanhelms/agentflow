package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/auth"
	"github.com/nscaledev/devflow/internal/commit"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/event"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/legacy"
)

// prepareWorkflowTasks binds reads to the runner identity. The Jira endpoint
// comes from the existing repository variable, paired with its credentials.
func (a *App) prepareWorkflowTasks() error {
	switch name := a.Cfg.Providers.TaskManagement.Provider; name {
	case "jira":
		for _, key := range []string{"JIRA_BASE", "JIRA_USER", "JIRA_TOKEN"} {
			if os.Getenv(key) == "" {
				return fmt.Errorf("Jira task reads require %s in the workflow environment", key)
			}
		}
		a.jira = atlassian.New(auth.Atlassian{Site: os.Getenv("JIRA_BASE"), Email: os.Getenv("JIRA_USER"), Token: os.Getenv("JIRA_TOKEN")})
		if path := os.Getenv("FLOW_MAP"); path != "" && a.Cfg.FlowMap == config.DefaultFlowMap {
			a.Cfg.FlowMap = path
		}
	case "github-issues":
		if server := strings.TrimSuffix(os.Getenv("GITHUB_SERVER_URL"), "/"); server != "" && !strings.EqualFold(server, "https://github.com") {
			return errors.New("the GitHub Issues adapter requires a github.com runner identity")
		}
		if os.Getenv("GH_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") == "" {
			return errors.New("GitHub Issues task reads require GH_TOKEN with Issues read access to the runner repository")
		}
	case "asana":
		if strings.TrimSpace(os.Getenv("ASANA_ACCESS_TOKEN")) == "" {
			return errors.New("Asana task reads require ASANA_ACCESS_TOKEN in the workflow environment")
		}
	case "filesystem", "repository", "none":
	default:
		return fmt.Errorf("workflow task reads do not support task_management.provider %q", name)
	}
	return nil
}

// ValidateWorkflowMessage checks the same convention as the CLI. Jira's
// historical existence check covers the PR title. Other providers link
// explicit trailers in both PR bodies and commits.
func (a *App) ValidateWorkflowMessage(ctx context.Context, msg string, pr, skipLookups bool) error {
	var problems []error
	for _, issue := range commit.Validate(msg, a.Cfg.Commit, "") {
		problems = append(problems, errors.New(issue.Message))
	}
	if len(problems) > 0 || skipLookups || event.OptedOutText(msg) {
		return errors.Join(problems...)
	}
	subject, body, _ := strings.Cut(msg, "\n")
	var keys []string
	var err error
	switch {
	case a.UsesTaskRefs():
		keys, err = a.taskKeys(body)
	case a.Cfg.Providers.TaskManagement.Provider == "jira" && pr:
		keys = commit.KeysIn(subject)
	}
	if err != nil || len(keys) == 0 {
		return err
	}
	if err := a.prepareWorkflowTasks(); err != nil {
		return err
	}
	for _, key := range keys {
		if err := a.checkWorkflowTask(ctx, key); err != nil {
			problems = append(problems, fmt.Errorf("verify %q with %s (check the reference and runner read access): %w", key, a.Cfg.Providers.TaskManagement.Provider, err))
		}
	}
	return errors.Join(problems...)
}

func (a *App) checkWorkflowTask(ctx context.Context, key string) error {
	if a.Cfg.Providers.TaskManagement.Provider == "jira" {
		tasks := legacy.NewJira("jira", a.jira.Base, a.jira, nil)
		_, err := tasks.Get(ctx, provider.Ref{Instance: "jira", Scope: projectOf(key), ID: key})
		return err
	}
	ref, err := a.workflowTaskRef(ctx, key)
	if err != nil {
		return err
	}
	_, err = a.getTask(ctx, ref)
	return err
}

// PlanShippedTask binds delivery previews to the configured destination.
func (a *App) PlanShippedTask(ctx context.Context, key, lifecycle string) (TransitionResult, error) {
	if a.Cfg.Providers.TaskManagement.Provider == "none" {
		return TransitionResult{Key: key, Event: lifecycle, Note: "task provider disabled"}, nil
	}
	if a.UsesTaskRefs() {
		if _, err := a.workflowTaskRef(ctx, key); err != nil {
			return TransitionResult{}, err
		}
	}
	if err := a.prepareWorkflowTasks(); err != nil {
		return TransitionResult{}, err
	}
	return a.PlanTransition(ctx, key, lifecycle)
}

func (a *App) workflowTaskRef(ctx context.Context, key string) (provider.Ref, error) {
	ref, err := a.taskRef(ctx, key)
	if err != nil {
		return ref, err
	}
	if a.usesAsana() {
		return a.validateAsanaRef(ref)
	}
	var scope string
	if a.usesIssues() {
		scope, err = a.defaultIssueRepo(ctx)
		if err != nil {
			return ref, err
		}
		if !strings.EqualFold(scope, os.Getenv("GITHUB_REPOSITORY")) {
			return ref, errors.New("task_management.settings.repository must match GITHUB_REPOSITORY")
		}
	} else {
		scope = a.fileScope()
	}
	if ref.Scope != scope {
		return ref, fmt.Errorf("expected configured task scope %q", scope)
	}
	return ref, nil
}

// ShippedKeys uses the CLI's task extraction with delivery opt-outs, an
// optional release-unit path and the sorted output of actions/ship.
func (a *App) ShippedKeys(ctx context.Context, rng, path string) ([]string, error) {
	if a.Cfg.Providers.TaskManagement.Provider == "none" {
		return nil, nil
	}
	msgs, err := a.Repo.Messages(ctx, rng, path)
	if err != nil {
		return nil, err
	}
	return a.keysInMessages(msgs, true)
}

func (a *App) keysInMessages(msgs []string, shipped bool) ([]string, error) {
	seen := map[string]bool{}
	var keys []string
	for _, msg := range msgs {
		if shipped && event.OptedOutText(msg) {
			continue
		}
		var found []string
		switch {
		case a.UsesTaskRefs():
			_, body, _ := strings.Cut(msg, "\n")
			var err error
			found, err = a.taskKeys(body)
			if err != nil {
				return nil, err
			}
		case shipped:
			found = commit.KeysIn(msg)
		default:
			found = gitx.KeyRe.FindAllString(msg, -1)
		}
		for _, key := range found {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	if shipped {
		slices.Sort(keys)
	}
	return keys, nil
}
