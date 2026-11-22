package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/legacy"
	"github.com/nscaledev/devflow/internal/provider/native"
	"github.com/nscaledev/devflow/internal/provider/nativemap"
	"github.com/nscaledev/devflow/internal/provider/notifications"
	"github.com/nscaledev/devflow/internal/provider/twg"
)

type jiraWrite taskWritePreview

func jiraMCPOrigin(site string) bool {
	u, err := url.Parse(site)
	return err == nil && u.Scheme == "https" && (u.EscapedPath() == "" || u.EscapedPath() == "/") &&
		!strings.Contains(site, "#")
}

func prepareJiraWrite(ctx context.Context, call native.Call) (*jiraWrite, error) {
	binding, err := native.PrepareWrite(ctx, call)
	if err != nil {
		return nil, err
	}
	return &jiraWrite{Binding: binding, Call: call}, nil
}

func (w *jiraWrite) run(ctx context.Context) (json.RawMessage, error) {
	raw, err := native.Write(ctx, w.Binding, w.Call)
	if err != nil {
		return nil, err
	}
	payload, err := native.Payload(raw)
	if err != nil {
		return nil, errors.Join(native.ErrWriteUncertain, err)
	}
	if err := nativemap.CheckJiraWriteResult(payload); err != nil {
		return nil, errors.Join(native.ErrWriteUncertain, err)
	}
	return payload, nil
}

func (w *jiraWrite) commentID(ctx context.Context, ref provider.Ref, body string) (string, error) {
	payload, err := w.run(ctx)
	if err != nil {
		return "", err
	}
	id, err := nativemap.DecodeJiraCommentReceipt(payload, ref, body)
	if err != nil {
		return "", errors.Join(native.ErrWriteUncertain, err)
	}
	return id, nil
}

func jiraRemoteSummary(summary any, write *jiraWrite) any {
	if write == nil {
		return summary
	}
	return struct {
		Intent any        `json:"intent"`
		Native *jiraWrite `json:"native"`
	}{summary, write}
}

func (a *App) jiraWriteScope(scope string) error {
	sel := a.Cfg.Providers.TaskManagement
	settings := a.jiraSettings()
	projectScope, _ := sel.RepositorySettings("projects").(*config.JiraSettings)
	siteScope, _ := sel.RepositorySettings("site").(*config.JiraSettings)
	if projectScope != nil {
		// Project keys are meaningful only at the site that scopes them.
		siteScope = projectScope
	}
	if siteScope != nil && strings.TrimRight(siteScope.Site, "/") != strings.TrimRight(settings.Site, "/") {
		return fmt.Errorf("jira site differs from the configured repository site: %w", native.ErrDenied)
	}
	contains := func(projects []string) bool {
		return slices.ContainsFunc(projects, func(project string) bool { return strings.EqualFold(project, scope) })
	}
	_, configured := sel.Sources["projects"]
	if projectScope != nil && !contains(projectScope.Projects) ||
		(configured || len(settings.Projects) != 0) && !contains(settings.Projects) {
		return fmt.Errorf("jira project %q is outside task_management.settings.projects: %w", scope, native.ErrDenied)
	}
	if err := sel.CheckTarget("projects", scope); err != nil {
		return fmt.Errorf("jira project %q: %w", scope, errors.Join(native.ErrDenied, err))
	}
	return nil
}

func jiraNativeRead[T any](ctx context.Context, write *jiraWrite, call native.Call, decode func(json.RawMessage) (T, error)) (T, error) {
	read := native.ReadForWrite
	if write != nil {
		read = func(ctx context.Context, call native.Call) (json.RawMessage, error) {
			return native.ReadBound(ctx, write.Binding, call)
		}
	}
	raw, err := read(ctx, call)
	if errors.Is(err, native.ErrUnavailable) {
		switch call.Tool {
		case "listJiraIssueRemoteIssueLinks", "listJiraIssueTransitions", "getJiraCurrentUser":
			raw, err = read(ctx, nativemap.JiraDeferredRead(call))
		}
	}
	var zero T
	if err != nil {
		return zero, err
	}
	payload, err := native.Payload(raw)
	if err != nil {
		return zero, err
	}
	return decode(payload)
}

func jiraNativeIssue(ctx context.Context, write *jiraWrite, site string, ref provider.Ref) (nativemap.JiraIssueSnapshot, error) {
	call, err := nativemap.JiraWriteIssue(site, ref)
	if err != nil {
		return nativemap.JiraIssueSnapshot{}, err
	}
	return jiraNativeRead(ctx, write, call, func(raw json.RawMessage) (nativemap.JiraIssueSnapshot, error) {
		return nativemap.DecodeJiraWriteIssue(raw, ref)
	})
}

func jiraNativeAccount(ctx context.Context, write *jiraWrite, site string) (string, error) {
	call, err := nativemap.JiraCurrentUser(site)
	if err != nil {
		return "", err
	}
	return jiraNativeRead(ctx, write, call, nativemap.DecodeJiraCurrentUser)
}

func (w *jiraWrite) checkIssueIdentity(ctx context.Context, site string, ref provider.Ref) error {
	call, err := nativemap.JiraWriteIssue(site, ref)
	if err != nil {
		return err
	}
	_, err = jiraNativeRead(ctx, w, call, func(raw json.RawMessage) (struct{}, error) {
		return struct{}{}, nativemap.CheckJiraIssueIdentity(raw, ref)
	})
	return err
}

func (a *App) prepareJiraComment(ctx context.Context, site string, ref provider.Ref, body string) (*jiraWrite, error) {
	if err := a.jiraWriteScope(ref.Scope); err != nil {
		return nil, err
	}
	if !jiraMCPOrigin(site) {
		return nil, nil
	}
	call, err := nativemap.JiraComment(site, ref, body)
	if err != nil {
		return nil, err
	}
	write, err := prepareJiraWrite(ctx, call)
	if errors.Is(err, native.ErrUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if write.Binding.Owner == "devflow" {
		err = write.checkIssueIdentity(ctx, site, ref)
	} else {
		_, err = jiraNativeIssue(ctx, write, site, ref)
	}
	return write, err
}

func (a *App) prepareJiraCreate(ctx context.Context, site string, draft provider.TaskDraft) (*jiraWrite, error) {
	if err := a.jiraWriteScope(draft.Scope); err != nil {
		return nil, err
	}
	for _, link := range draft.Links {
		if link.Ref != nil {
			if err := a.jiraWriteScope(link.Ref.Scope); err != nil {
				return nil, err
			}
		}
	}
	if !jiraMCPOrigin(site) || len(draft.Links) != 0 {
		return nil, nil
	}
	call, err := nativemap.JiraCreate(site, draft)
	if err != nil {
		return nil, err
	}
	write, err := prepareJiraWrite(ctx, call)
	if errors.Is(err, native.ErrUnavailable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if write.Binding.Owner == "devflow" {
		read, err := nativemap.JiraProjectIssueTypes(site, draft.Scope)
		if err != nil {
			return nil, err
		}
		if _, err := jiraNativeRead(ctx, write, read, func(raw json.RawMessage) (struct{}, error) {
			return struct{}{}, nativemap.CheckJiraCreateType(raw, draft.Type)
		}); err != nil {
			return nil, err
		}
	}
	return write, nil
}

type jiraTransition struct {
	Write *jiraWrite `json:"write"`
	from  nativemap.JiraIssueSnapshot
	toID  string
}

func (a *App) prepareJiraTransition(ctx context.Context, site string, ref provider.Ref, fm *flowmap.Map, plan TransitionResult, issue nativemap.JiraIssueSnapshot) (TransitionResult, *jiraTransition, error) {
	plan = jiraPlanFromStatus(fm, ref.Scope, plan, issue.Status.Name, issue.Status.Category.Key == "done")
	if plan.To == "" {
		return plan, nil, nil
	}
	selected, err := jiraTransitionID(ctx, nil, site, ref, issue.Status.Category.Key, plan.To)
	if errors.Is(err, legacy.ErrNoTransition) {
		plan.Note = fmt.Sprintf("no transition from %q to %q in this workflow", plan.From, plan.To)
		return plan, nil, nil
	}
	if err != nil {
		return plan, nil, err
	}
	call, err := nativemap.JiraMove(site, ref, selected)
	if err != nil {
		return plan, nil, err
	}
	write, err := prepareJiraWrite(ctx, call)
	if err != nil {
		return plan, nil, err
	}
	return plan, &jiraTransition{Write: write, from: issue, toID: selected}, nil
}

func jiraTransitionID(ctx context.Context, write *jiraWrite, site string, ref provider.Ref, category, target string) (string, error) {
	call, err := nativemap.JiraAvailableTransitions(site, ref)
	if err != nil {
		return "", err
	}
	transitions, err := jiraNativeRead(ctx, write, call, nativemap.DecodeJiraTransitions)
	if err != nil {
		return "", err
	}
	fromRank := jiraCategoryRank(category)
	for _, transition := range transitions {
		if transition.ID == "" || !strings.EqualFold(transition.To.Name, target) ||
			(transition.Available != nil && !*transition.Available) {
			continue
		}
		toRank := jiraCategoryRank(transition.To.Category.Key)
		if fromRank < 0 || toRank < fromRank {
			return "", fmt.Errorf("jira transition to %q is not verifiably forward: %w", target, provider.ErrConflict)
		}
		return transition.ID, nil
	}
	return "", legacy.ErrNoTransition
}

func jiraCategoryRank(category string) int {
	return slices.Index([]string{"new", "indeterminate", "done"}, category)
}

func (a *App) runJiraTransition(ctx context.Context, site string, ref provider.Ref, plan TransitionResult, prepared *jiraTransition) (TransitionResult, error) {
	current, err := jiraNativeIssue(ctx, prepared.Write, site, ref)
	if err == nil && strings.EqualFold(current.Status.Name, plan.To) {
		plan.Note = "already in " + plan.To
		return plan, nil
	}
	if err == nil && current.Status != prepared.from.Status {
		err = fmt.Errorf("jira issue changed status before the transition: %w", provider.ErrConflict)
	}
	if err == nil {
		var selected string
		selected, err = jiraTransitionID(ctx, prepared.Write, site, ref, current.Status.Category.Key, plan.To)
		if err == nil && selected != prepared.toID {
			err = fmt.Errorf("jira workflow changed before the transition: %w", provider.ErrConflict)
		}
	}
	if err == nil {
		_, err = prepared.Write.run(ctx)
		plan.Applied = err == nil
	}
	a.audit(policy.Remote, "transition", map[string]any{"site": site, "transition": plan, "native": prepared.Write}, err)
	return plan, err
}

func (a *App) tryNativeJiraTransition(ctx context.Context, ref provider.Ref, event string, dryRun bool, token string) (any, bool, error) {
	site, err := a.jiraSite()
	if err != nil {
		return nil, true, err
	}
	if err := a.jiraWriteScope(ref.Scope); err != nil {
		return nil, true, err
	}
	if !jiraMCPOrigin(site) {
		return nil, false, nil
	}
	issue, err := jiraNativeIssue(ctx, nil, site, ref)
	if errors.Is(err, native.ErrUnavailable) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	fm, err := a.FlowMap(ctx)
	if err != nil {
		return nil, true, err
	}
	plan := jiraTransitionTarget(fm, ref.ID, event)
	if plan.To == "" {
		return plan, true, nil
	}
	if dryRun {
		return jiraPlanFromStatus(fm, ref.Scope, plan, issue.Status.Name, issue.Status.Category.Key == "done"), true, nil
	}
	plan, prepared, err := a.prepareJiraTransition(ctx, site, ref, fm, plan, issue)
	if err != nil || prepared == nil {
		return plan, true, err
	}
	summary := jiraRemoteSummary(struct {
		TransitionResult
		Site string `json:"site"`
	}{plan, site}, prepared.Write)
	commands := []string{"devflow transition " + ref.ID + " " + shellQuote(event)}
	if p, err := a.gate(policy.Remote, "transition", summary, token, false, commands); p != nil || err != nil {
		return p, true, err
	}
	result, err := a.runJiraTransition(ctx, site, ref, plan, prepared)
	return result, true, err
}

type jiraStartWrites struct {
	Assignment *jiraWrite      `json:"assignment"`
	Transition *jiraTransition `json:"transition,omitempty"`
	accountID  string
}

func (a *App) prepareJiraStart(ctx context.Context, site string, ref provider.Ref) (string, TransitionResult, *jiraStartWrites, error) {
	var plan TransitionResult
	if err := a.jiraWriteScope(ref.Scope); err != nil {
		return "", plan, nil, err
	}
	if !jiraMCPOrigin(site) {
		return "", plan, nil, nil
	}
	_, err := jiraNativeIssue(ctx, nil, site, ref)
	if errors.Is(err, native.ErrUnavailable) {
		return "", plan, nil, nil
	}
	if err != nil {
		return "", plan, nil, err
	}
	account, err := jiraNativeAccount(ctx, nil, site)
	if err != nil {
		return "", plan, nil, err
	}
	assignCall, err := nativemap.JiraAssign(site, ref, account)
	if err != nil {
		return "", plan, nil, err
	}
	assignment, err := prepareJiraWrite(ctx, assignCall)
	if err != nil {
		return "", plan, nil, err
	}
	writes := &jiraStartWrites{Assignment: assignment, accountID: account}
	if err := writes.checkAccount(ctx, site); err != nil {
		return "", plan, nil, err
	}
	issue, err := jiraNativeIssue(ctx, assignment, site, ref)
	if err != nil {
		return "", plan, nil, err
	}
	fm, err := a.FlowMap(ctx)
	if err != nil {
		// Starting without a flow map still creates and assigns the branch.
		plan.Note = err.Error()
		return issue.Title, plan, writes, nil
	}
	plan = jiraTransitionTarget(fm, ref.ID, flowmap.BranchCreated)
	if plan.To != "" {
		plan, writes.Transition, err = a.prepareJiraTransition(ctx, site, ref, fm, plan, issue)
		if err != nil {
			return "", plan, nil, err
		}
		if writes.Transition != nil {
			if err := checkJiraAccount(ctx, writes.Transition.Write, site, account); err != nil {
				return "", plan, nil, err
			}
		}
	}
	return issue.Title, plan, writes, nil
}

func (w *jiraStartWrites) checkAccount(ctx context.Context, site string) error {
	return checkJiraAccount(ctx, w.Assignment, site, w.accountID)
}

func checkJiraAccount(ctx context.Context, write *jiraWrite, site, expected string) error {
	actual, err := jiraNativeAccount(ctx, write, site)
	if err == nil && actual != expected {
		return fmt.Errorf("authenticated jira account differs across the prepared operation: %w", provider.ErrConflict)
	}
	return err
}

func (w *jiraStartWrites) assign(ctx context.Context, site string, ref provider.Ref) error {
	if err := w.checkAccount(ctx, site); err != nil {
		return err
	}
	issue, err := jiraNativeIssue(ctx, w.Assignment, site, ref)
	if err != nil || issue.AccountID == w.accountID {
		return err
	}
	_, err = w.Assignment.run(ctx)
	return err
}

// LinkJiraRemote adds a web link to an existing Jira work item.
func (a *App) LinkJiraRemote(ctx context.Context, key, linkURL, title, token string) (any, error) {
	key, err := a.keyOrCurrent(ctx, key)
	if err != nil {
		return nil, err
	}
	ref, err := legacy.JiraRef("jira", key)
	if err != nil {
		return nil, err
	}
	site, err := a.jiraSite()
	if err != nil {
		return nil, err
	}
	if err := a.jiraWriteScope(ref.Scope); err != nil {
		return nil, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = linkURL
	}
	call, err := nativemap.JiraLinkRemote(site, ref, linkURL, title)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(fmt.Appendf(nil, "%q %q %q %q", strings.TrimRight(site, "/"), ref.String(), linkURL, title))
	globalID := fmt.Sprintf("devflow:jira-remote-link:%x", digest)
	intent := map[string]any{"site": site, "issue": key, "url": linkURL, "title": title, "operation_id": globalID}
	backend, read, apply, err := a.prepareJiraRemoteLink(ctx, site, ref, call, linkURL, title, globalID, intent)
	if err != nil {
		return nil, err
	}
	intent["backend"] = backend
	exists, err := read()
	if err != nil {
		return nil, err
	}
	alreadyApplied := map[string]any{"key": key, "linked": true, "status": provider.AlreadyApplied}
	if exists && token == "" {
		return alreadyApplied, nil
	}
	if p, err := a.gate(policy.Remote, "jira_remote_link", intent, token, false, nil); p != nil || err != nil {
		return p, err
	}
	if exists {
		return alreadyApplied, nil
	}

	store := notifications.FileStore{Dir: filepath.Join(a.Home, "task-write-receipts")}
	receipt := notifications.ReceiptKey{OperationID: globalID, Destination: "jira-remote-link"}
	state, err := store.Claim(ctx, receipt, digest)
	if err != nil {
		return nil, fmt.Errorf("claim jira web link: %w", err)
	}
	if state != notifications.Claimed {
		return nil, fmt.Errorf("%w: jira web link was claimed but is absent from the current issue", native.ErrWriteUncertain)
	}
	if err = apply(); errors.Is(err, native.ErrWriteNotDispatched) {
		err = errors.Join(err, store.Finish(context.WithoutCancel(ctx), receipt, notifications.Retryable))
	} else if err == nil {
		exists, err = read()
		switch {
		case err != nil:
			err = errors.Join(native.ErrWriteUncertain, err)
		case !exists:
			err = fmt.Errorf("%w: jira did not return the approved web link", native.ErrWriteUncertain)
		default:
			err = store.Finish(context.WithoutCancel(ctx), receipt, notifications.Delivered)
		}
	}
	a.audit(policy.Remote, "jira_remote_link", intent, err)
	return map[string]any{"key": key, "linked": err == nil}, err
}

func (a *App) prepareJiraRemoteLink(ctx context.Context, site string, ref provider.Ref, call native.Call, linkURL, title, globalID string,
	intent map[string]any,
) (string, func() (bool, error), func() error, error) {
	find := func(links []atlassian.Page, err error) (bool, error) {
		return slices.ContainsFunc(links, func(link atlassian.Page) bool {
			return link.URL == linkURL && link.Title == title
		}), err
	}
	write, err := prepareJiraWrite(ctx, call)
	switch {
	case err == nil:
		if err := write.checkIssueIdentity(ctx, site, ref); err != nil {
			return "", nil, nil, err
		}
		list, err := nativemap.JiraRemoteLinks(site, ref)
		if err != nil {
			return "", nil, nil, err
		}
		intent["native"] = write
		return "mcp", func() (bool, error) {
				return find(jiraNativeRead(ctx, write, list, nativemap.DecodeJiraRemoteLinks))
			}, func() error {
				_, err := write.run(ctx)
				return err
			}, nil
	case !errors.Is(err, native.ErrUnavailable):
		return "", nil, nil, err
	}
	cli, err := twg.NewJira("jira", site, a.jiraSettings().Projects)
	if err != nil {
		return "", nil, nil, err
	}
	return "twg", func() (bool, error) {
			links, err := cli.RemoteLinks(ctx, ref)
			if errors.Is(err, twg.ErrToolAbsent) {
				return false, fmt.Errorf("%w: connect the Atlassian MCP full tool catalog or install and sign in to the Teamwork Graph CLI", native.ErrUnavailable)
			}
			return find(links, atlCLIError("jira", err))
		}, func() error {
			return atlCLIError("jira", cli.AddRemoteLink(ctx, ref, linkURL, title, globalID))
		}, nil
}
