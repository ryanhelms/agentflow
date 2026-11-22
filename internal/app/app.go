// Package app implements devflow's operations. The CLI, the MCP server and
// the hooks are thin layers over these methods.
package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/nscaledev/devflow/internal/atlassian"
	"github.com/nscaledev/devflow/internal/auth"
	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/contextx"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/gh"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
)

// App is an opened repository plus the developer's configuration.
type App struct {
	Repo   *gitx.Repo
	Cfg    *config.Repo
	HasCfg bool
	User   *config.User
	Gate   *policy.Gate
	Client string
	Home   string

	tasks provider.Tasks
	jira  *atlassian.Client
	ghc   *gh.Client
	fm    *flowmap.Map
}

// Open opens the repo containing dir. client names the calling AI client.
func Open(ctx context.Context, dir, client string) (*App, error) {
	repo, err := gitx.Open(ctx, dir)
	if err != nil {
		return nil, err
	}
	cfg, found, err := config.LoadRepo(repo.Dir)
	if err != nil {
		return nil, err
	}
	user, err := config.LoadUser()
	if err != nil {
		return nil, err
	}
	home, err := config.Home()
	if err != nil {
		return nil, err
	}
	if client == "" {
		client = "cli"
	}
	gate, err := policy.NewGate(home, config.Effective(user, cfg), client)
	if err != nil {
		return nil, err
	}
	return &App{Repo: repo, Cfg: cfg, HasCfg: found, User: user, Gate: gate, Client: client, Home: home}, nil
}

// Jira returns an authenticated Atlassian client.
func (a *App) Jira() (*atlassian.Client, error) {
	if a.jira != nil {
		return a.jira, nil
	}
	cred, err := auth.LoadAtlassian(a.Cfg.Jira.Site)
	if err != nil {
		return nil, err
	}
	if cred.Site == "" {
		cred.Site = a.Cfg.Jira.Site
	}
	a.jira = atlassian.New(cred)
	return a.jira, nil
}

// GitHub returns a client using the developer's gh token.
func (a *App) GitHub(ctx context.Context) (*gh.Client, error) {
	if a.ghc != nil {
		return a.ghc, nil
	}
	tok, err := auth.GitHubToken(ctx)
	if err != nil {
		return nil, err
	}
	a.ghc = gh.New(tok)
	return a.ghc, nil
}

// FlowMap loads the flow map (cached for 15 minutes).
func (a *App) FlowMap(ctx context.Context) (*flowmap.Map, error) {
	if a.fm != nil {
		return a.fm, nil
	}
	fetch := func(repo, ref, path string) ([]byte, error) {
		c, err := a.GitHub(ctx)
		if err != nil {
			return nil, err
		}
		b, _, err := c.File(ctx, repo, ref, path, "")
		return b, err
	}
	m, err := flowmap.Load(a.Cfg.FlowMap, a.Repo.Dir, filepath.Join(a.Home, "cache"), 15*time.Minute, fetch)
	if err != nil {
		return nil, err
	}
	a.fm = m
	return m, nil
}

// GitHubRepo returns origin as owner/name.
func (a *App) GitHubRepo(ctx context.Context) (string, error) {
	r := a.Repo.RemoteRepo(ctx)
	if r == "" {
		return "", errors.New("origin is not a GitHub remote")
	}
	return r, nil
}

// CurrentKey returns the resolved task key for the current branch, or "".
func (a *App) CurrentKey(ctx context.Context) string {
	if a.usesIssues() {
		if ref, err := a.branchIssue(ctx); err == nil && ref.ID != "" {
			return issueKey(ref)
		}
		return ""
	}
	return gitx.KeyFromBranch(a.Repo.Branch(ctx))
}

func (a *App) keyOrCurrent(ctx context.Context, key string) (string, error) {
	if a.usesIssues() {
		ref, err := a.issueRef(ctx, key)
		if err != nil {
			return "", err
		}
		return issueKey(ref), nil
	}
	if key != "" {
		return key, nil
	}
	if k := a.CurrentKey(ctx); k != "" {
		return k, nil
	}
	return "", errors.New("no Jira key given and the branch name has none; pass a key or run devflow start")
}

// Checklists returns the local acceptance-criteria store.
func (a *App) Checklists(ctx context.Context) contextx.Store {
	return contextx.Store{Dir: filepath.Join(a.Repo.GitDir(ctx), "devflow")}
}

// Pending is returned instead of a result when an action did not run yet.
type Pending struct {
	Status       string   `json:"status"` // needs_confirmation | suggest
	ConfirmToken string   `json:"confirm_token,omitempty"`
	Reason       string   `json:"reason"`
	Summary      any      `json:"summary"`
	Commands     []string `json:"commands,omitempty"`
}

// gate checks autonomy. It returns a non-nil Pending when the action must not run now.
func (a *App) gate(class policy.Class, action string, summary any, token string, force bool, commands []string) (*Pending, error) {
	d, err := a.Gate.Check(class, action, summary, token, force)
	if err != nil {
		return nil, err
	}
	switch d.Outcome {
	case policy.Proceed:
		return nil, nil
	case policy.SuggestOnly:
		return &Pending{Status: "suggest", Reason: d.Reason, Summary: summary, Commands: commands}, nil
	}
	return &Pending{Status: "needs_confirmation", ConfirmToken: d.Token, Reason: d.Reason, Summary: summary}, nil
}

func (a *App) audit(class policy.Class, action string, payload any, err error) {
	result := "ok"
	if err != nil {
		result = "error: " + err.Error()
	}
	a.Gate.Audit(class, action, payload, result)
}

// Transition moves key to the status mapped for event ("pr.merged", "#qa").
// It only moves forward: done-category issues and no-op mappings are skipped.
type TransitionResult struct {
	operationID string
	Key         string `json:"key"`
	Event       string `json:"event"`
	From        string `json:"from"`
	To          string `json:"to,omitempty"`
	Applied     bool   `json:"applied"`
	Note        string `json:"note,omitempty"`
}

// PlanTransition works out what event would do to key without changing anything.
func (a *App) PlanTransition(ctx context.Context, key, event string) (TransitionResult, error) {
	if a.usesIssues() {
		return a.planIssueTransition(ctx, key, event, nil)
	}
	res := TransitionResult{Key: key, Event: event}
	fm, err := a.FlowMap(ctx)
	if err != nil {
		return res, err
	}
	target, ok := fm.Resolve(projectOf(key), event)
	if !ok {
		res.Note = "no mapping for this project; nothing happens"
		return res, nil
	}
	res.To = target
	jc, err := a.Jira()
	if err != nil {
		res.Note = "Jira not reachable: " + err.Error()
		return res, nil
	}
	iss, err := jc.GetIssue(ctx, key)
	if err != nil {
		return res, err
	}
	res.From = iss.Fields.Status.Name
	switch {
	case fm.Backwards(projectOf(key), res.From, target):
		res.Note = fmt.Sprintf("%s is before %s in the project's order; devflow only moves forward", target, res.From)
		res.To = ""
	case res.From == target:
		res.Note = "already in " + target
		res.To = ""
	case iss.Fields.Status.Category.Key == "done":
		res.Note = "ticket is done; devflow never moves it back"
		res.To = ""
	}
	return res, nil
}

// ApplyTransition performs a planned transition (no gate; callers gate first).
func (a *App) ApplyTransition(ctx context.Context, plan TransitionResult) (TransitionResult, error) {
	if plan.To == "" {
		return plan, nil
	}
	if a.usesIssues() {
		return a.applyIssueTransition(ctx, plan)
	}
	jc, err := a.Jira()
	if err != nil {
		return plan, err
	}
	ts, err := jc.Transitions(ctx, plan.Key)
	if err != nil {
		return plan, err
	}
	for _, t := range ts {
		if t.To.Name == plan.To {
			err := jc.DoTransition(ctx, plan.Key, t.ID)
			a.audit(policy.Remote, "transition", plan, err)
			if err == nil {
				plan.Applied = true
			}
			return plan, err
		}
	}
	plan.Note = fmt.Sprintf("no transition from %q to %q in this workflow", plan.From, plan.To)
	return plan, nil
}

// Transition plans and, subject to the gate, applies a transition.
func (a *App) Transition(ctx context.Context, key, event string, dryRun bool, token string) (any, error) {
	return a.transition(ctx, key, event, dryRun, token, "")
}

func (a *App) transition(ctx context.Context, key, event string, dryRun bool, token, operationID string) (any, error) {
	key, err := a.keyOrCurrent(ctx, key)
	if err != nil {
		return nil, err
	}
	plan, err := a.PlanTransition(ctx, key, event)
	plan.operationID = operationID
	if err != nil || dryRun || plan.To == "" {
		return plan, err
	}
	var summary any = plan
	commands := []string{fmt.Sprintf("devflow transition %s %s", key, event)}
	if a.usesIssues() {
		ref, err := a.issueRef(ctx, plan.Key)
		if err != nil {
			return nil, err
		}
		summary = struct {
			TransitionResult
			TaskRef string `json:"task_ref"`
		}{plan, ref.String()}
		// A structured suggestion preserves literal event names such as "#done".
		commands = nil
	}
	if p, err := a.gate(policy.Remote, "transition", summary, token, false, commands); p != nil || err != nil {
		return p, err
	}
	return a.ApplyTransition(ctx, plan)
}

// Comment posts a comment on a ticket.
func (a *App) Comment(ctx context.Context, key, text, token string) (any, error) {
	if a.usesIssues() {
		return a.commentIssue(ctx, key, text, token)
	}
	key, err := a.keyOrCurrent(ctx, key)
	if err != nil {
		return nil, err
	}
	summary := map[string]string{"key": key, "comment": text}
	if p, err := a.gate(policy.Remote, "comment", summary, token, false, nil); p != nil || err != nil {
		return p, err
	}
	jc, err := a.Jira()
	if err != nil {
		return nil, err
	}
	err = jc.AddComment(ctx, key, text+viaNote(a))
	a.audit(policy.Remote, "comment", summary, err)
	return map[string]any{"key": key, "commented": err == nil}, err
}

func viaNote(a *App) string {
	if a.Gate.Autonomy.Remote == config.Auto {
		return "\n\n(via devflow)"
	}
	return ""
}

func projectOf(key string) string {
	for i := range key {
		if key[i] == '-' {
			return key[:i]
		}
	}
	return key
}
