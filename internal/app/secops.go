package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/gh"
	"github.com/nscaledev/agentflow/internal/gitx"
	"github.com/nscaledev/agentflow/internal/policy"
	"github.com/nscaledev/agentflow/internal/provider"
	"github.com/nscaledev/agentflow/internal/provider/githubissues"
	"github.com/nscaledev/agentflow/internal/provider/secops"
	"github.com/nscaledev/agentflow/internal/tasktemplate"
)

type SecOpsTriageInput struct {
	Source provider.Ref
	Since  time.Time
	Apply  bool
}

type SecOpsTaskPlan struct {
	Source      string             `json:"source"`
	Kind        secops.EventKind   `json:"kind"`
	Severity    string             `json:"severity"`
	Provider    string             `json:"provider"`
	OperationID string             `json:"operation_id"`
	Task        provider.TaskDraft `json:"task"`
}

var secOpsSeverities = []string{"low", "medium", "high", "critical"}

// SecOpsTriage previews configured security events or creates a task for one
// explicit source under the normal task creation approval policy.
func (a *App) SecOpsTriage(ctx context.Context, in SecOpsTriageInput, token string) (any, error) {
	if in.Source != (provider.Ref{}) {
		if err := in.Source.Validate(); err != nil {
			return nil, err
		}
	} else if in.Apply {
		return nil, errors.New("secops apply requires an explicit source reference")
	}
	plans := []SecOpsTaskPlan{}
	settings, ok := a.Cfg.Providers.SecOps.Settings.(*config.GitHubSecOpsSettings)
	if !ok {
		if in.Apply {
			return nil, errors.New("secops is disabled")
		}
		return plans, nil
	}
	instance := a.Cfg.Providers.TaskManagement.Provider
	if instance == "none" && !in.Apply {
		return plans, nil
	}
	if !a.usesIssues() && !a.usesFileTasks() {
		return nil, provider.Unsupported(instance, "replayable secops task creation")
	}
	t, err := a.ResolveTaskTemplate(tasktemplate.Ref{})
	if err != nil {
		return nil, err
	}
	target, err := a.secOpsTarget(ctx, t)
	if err != nil {
		return nil, err
	}
	events, err := a.secOpsEvents(ctx, settings, in)
	if err != nil {
		return nil, err
	}
	minimum := slices.Index(secOpsSeverities, settings.MinSeverity)
	seen := map[provider.Ref]bool{}
	for _, event := range events {
		if (in.Source != (provider.Ref{}) && in.Source != event.Ref) ||
			slices.Index(secOpsSeverities, event.Severity) < minimum || seen[event.Ref] {
			continue
		}
		seen[event.Ref] = true
		plan, err := a.planSecOpsTask(event, target, t)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	if !in.Apply {
		return plans, nil
	}
	if len(plans) != 1 {
		return nil, fmt.Errorf("no eligible security event for %s", in.Source)
	}
	return a.writeSecOpsTask(ctx, plans[0], token)
}

func (a *App) secOpsEvents(ctx context.Context, settings *config.GitHubSecOpsSettings, in SecOpsTriageInput) ([]secops.Event, error) {
	var events []secops.Event
	if len(settings.Findings) != 0 && (in.Source.Instance == "" || in.Source.Instance == "github") {
		repo, err := a.secOpsRepository(ctx, settings.Repository)
		if err != nil {
			return nil, err
		}
		client, err := a.secOpsGitHub(ctx)
		if err != nil {
			return nil, err
		}
		for _, kind := range settings.Findings {
			if in.Source.Instance != "" && (in.Source.Scope != repo || !strings.HasPrefix(in.Source.ID, kind+":")) {
				continue
			}
			source, err := secops.NewGitHubFindings(client, "github", repo, secops.GitHubAlertSource(kind))
			if err != nil {
				return nil, err
			}
			findings, err := source.Findings(ctx, in.Since)
			if err != nil {
				return nil, err
			}
			for _, finding := range findings {
				events = append(events, secops.Event{Kind: secops.FindingEvent, Ref: finding.Ref, Title: finding.Title, Body: finding.Body, Severity: finding.Severity, URL: finding.URL})
			}
		}
	}
	if settings.Incidents != nil && (in.Source.Instance == "" || in.Source.Instance == "incident-fixture") {
		incidents, err := a.secOpsIncidents(ctx, *settings.Incidents, in.Since)
		if err != nil {
			return nil, err
		}
		for _, incident := range incidents {
			events = append(events, secops.Event{Kind: secops.IncidentEvent, Ref: incident.Ref, Title: incident.Title, Body: incident.Body, Severity: incident.Severity, URL: incident.URL})
		}
	}
	return events, nil
}

func (a *App) secOpsGitHub(ctx context.Context) (*gh.Client, error) {
	if token := os.Getenv("AGENTFLOW_SECOPS_TOKEN"); token != "" {
		return gh.New(token), nil
	}
	return a.GitHub(ctx)
}

func (a *App) secOpsRepository(ctx context.Context, configured *string) (string, error) {
	if configured != nil {
		if err := gitx.ValidateGitHubSlug(*configured); err != nil {
			return "", fmt.Errorf("secops.settings.repository: %w", err)
		}
		return strings.ToLower(*configured), nil
	}
	d, err := a.detectForge(ctx)
	if err != nil {
		return "", err
	}
	if !d.OK() || d.Forge != gitx.ForgeGitHub || !strings.HasPrefix(d.Target.ID(), "github.com/") {
		return "", errors.New("no known github security repository; configure secops.settings.repository")
	}
	return strings.ToLower(d.Target.URL.Path), nil
}

func (a *App) secOpsIncidents(ctx context.Context, cfg config.SecOpsIncidentFixture, since time.Time) ([]provider.Incident, error) {
	root, err := os.OpenRoot(a.Repo.Dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	info, err := root.Stat(cfg.Fixture)
	if err != nil {
		return nil, fmt.Errorf("stat secops incident fixture: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("secops incident fixture requires a regular file")
	}
	raw, err := root.ReadFile(cfg.Fixture)
	if err != nil {
		return nil, fmt.Errorf("read secops incident fixture: %w", err)
	}
	source, err := secops.NewFixtureIncidents("incident-fixture", cfg.Scope, raw)
	if err != nil {
		return nil, err
	}
	return source.Incidents(ctx, since)
}

func (a *App) secOpsTarget(ctx context.Context, t *tasktemplate.Template) (secops.TaskTarget, error) {
	target := secops.TaskTarget{Instance: a.Cfg.Providers.TaskManagement.Provider}
	if t != nil && t.Defaults.Scope != nil {
		target.Scope = *t.Defaults.Scope
	}
	if a.usesFileTasks() {
		if t == nil || t.Defaults.Scope == nil {
			target.Scope = a.fileScope()
		}
		return target, nil
	}
	var err error
	if target.Scope == "" {
		target.Scope, err = a.defaultIssueRepo(ctx)
	} else {
		target.Scope, err = issueScope(target.Scope)
	}
	return target, err
}

func (a *App) planSecOpsTask(event secops.Event, target secops.TaskTarget, t *tasktemplate.Template) (SecOpsTaskPlan, error) {
	var plan secops.TaskPlan
	var err error
	if event.Kind == secops.FindingEvent {
		plan, err = secops.PlanFinding(provider.Finding{Ref: event.Ref, Title: event.Title, Body: event.Body, Severity: event.Severity, URL: event.URL}, target, nil)
	} else {
		plan, err = secops.PlanIncident(provider.Incident{Ref: event.Ref, Title: event.Title, Body: event.Body, Severity: event.Severity, URL: event.URL}, target, nil)
	}
	if err != nil {
		return SecOpsTaskPlan{}, err
	}
	draft := plan.Draft
	if a.usesIssues() {
		// GitHub Issues carries the source URL in Markdown.
		link := draft.Links[0]
		escaped := strings.NewReplacer("<", "%3C", ">", "%3E", " ", "%20").Replace(link.URL)
		if draft.Description == "" && t != nil && t.Defaults.Description != nil {
			draft.Description = *t.Defaults.Description
		}
		draft.Description = strings.TrimSpace(link.Title + ": <" + escaped + ">\n\n" + draft.Description)
		draft.Links = nil
		draft, err = a.resolveSecOpsIssueDraft(draft, t)
	} else {
		if t != nil {
			draft.Links = append(slices.Clone(t.Defaults.Links), draft.Links...)
		}
		draft, err = a.resolveSecOpsFileDraft(draft, t)
	}
	if err != nil {
		return SecOpsTaskPlan{}, fmt.Errorf("render task for %s: %w", event.Ref, err)
	}
	return SecOpsTaskPlan{Source: event.Ref.String(), Kind: event.Kind, Severity: event.Severity, Provider: target.Instance, OperationID: plan.Options.OperationID, Task: draft}, nil
}

func (a *App) resolveSecOpsIssueDraft(draft provider.TaskDraft, t *tasktemplate.Template) (provider.TaskDraft, error) {
	draft.Labels = a.issueSettings().Labels
	if t != nil {
		if err := t.CheckSupport(githubissues.SupportedFields()); err != nil {
			return draft, err
		}
		input := tasktemplate.Fields{Scope: &draft.Scope, Title: &draft.Title, Description: &draft.Description}
		if t.Defaults.Labels == nil {
			input.Labels = draft.Labels
		}
		var err error
		draft, err = t.Resolve(input)
		if err != nil {
			return draft, err
		}
		draft.Template = &t.Ref
	}
	return draft, githubissues.ValidateDraft(issuesInstance, draft)
}

func (a *App) resolveSecOpsFileDraft(draft provider.TaskDraft, t *tasktemplate.Template) (provider.TaskDraft, error) {
	if t == nil {
		draft.Type = "Task"
		return draft, nil
	}
	return a.resolveFileTaskDraft(draft, t.Ref)
}

func (a *App) writeSecOpsTask(ctx context.Context, plan SecOpsTaskPlan, token string) (any, error) {
	const action = "ticket_create"
	opts := provider.WriteOptions{OperationID: plan.OperationID}
	summary := map[string]any{"secops": plan}
	if a.usesFileTasks() {
		ref := provider.Ref{Instance: plan.Provider, Scope: plan.Task.Scope}
		result, pending, err := a.writeFileTask(ctx, action, summary, token, ref, func(tasks provider.Tasks) (provider.WriteResult, error) {
			return tasks.Create(ctx, plan.Task, opts)
		})
		if pending != nil || err != nil {
			return pending, err
		}
		return result, nil
	}
	if err := a.taskWriteScope(ctx, plan.Task.Scope); err != nil {
		return nil, err
	}
	if pending, err := a.gate(policy.Remote, action, summary, token, true, nil); pending != nil || err != nil {
		return pending, err
	}
	tasks, err := a.issueTasks(ctx)
	if err != nil {
		return nil, err
	}
	issues, ok := tasks.(*githubissues.Adapter)
	if !ok {
		return nil, provider.Unsupported(plan.Provider, "durably claim secops writes")
	}
	result, err := a.createSecOpsIssue(ctx, issues, plan.Task, opts)
	a.audit(policy.Remote, action, summary, err)
	return result, err
}

// The provider checks its persisted marker before acquiring the local claim.
// A claim survives process interruption and blocks a second POST while GitHub
// has yet to surface the issue. It contains only the opaque operation ID.
func (a *App) createSecOpsIssue(ctx context.Context, issues *githubissues.Adapter, draft provider.TaskDraft, opts provider.WriteOptions) (provider.WriteResult, error) {
	dir := filepath.Join(a.Home, "secops-operations")
	path := filepath.Join(dir, opts.OperationID)
	owned := false
	result, err := issues.CreateOnce(ctx, draft, opts, func() (bool, error) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return false, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		owned = true
		return true, f.Close()
	})
	if owned && err != nil && !errors.Is(err, githubissues.ErrWriteOutcomeUnknown) {
		err = errors.Join(err, os.Remove(path))
	}
	return result, err
}
