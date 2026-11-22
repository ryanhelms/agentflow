// Package app implements devflow's operations. The CLI, the MCP server and
// the hooks are thin layers over these methods.
package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
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
	"github.com/nscaledev/devflow/internal/provider/legacy"
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

	jiraMu sync.Mutex
	jira   *atlassian.Client
	tasks  provider.Tasks
	ghc    *gh.Client
	fm     *flowmap.Map
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

// GitHub returns a client using the developer's gh token.
func (a *App) GitHub(ctx context.Context) (*gh.Client, error) {
	if a.ghc != nil {
		return a.ghc, nil
	}
	tok, err := auth.GitHubToken(ctx, a.Repo.Dir)
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

func (a *App) detectForge(ctx context.Context) (gitx.Detection, error) {
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
	return a.Repo.DetectForge(ctx, opts)
}

// CurrentKey returns the resolved task key for the current branch, or "".
func (a *App) CurrentKey(ctx context.Context) string {
	if a.UsesTaskRefs() {
		if ref, err := a.branchTask(ctx); err == nil && ref.ID != "" {
			return taskKey(ref)
		}
		return ""
	}
	return gitx.KeyFromBranch(a.Repo.Branch(ctx))
}

func (a *App) keyOrCurrent(ctx context.Context, key string) (string, error) {
	if a.UsesTaskRefs() {
		ref, err := a.taskRef(ctx, key)
		if err != nil {
			return "", err
		}
		return taskKey(ref), nil
	}
	if key == "" {
		key = a.CurrentKey(ctx)
	}
	if key != "" {
		_, err := legacy.JiraRef("jira", key)
		return key, err
	}
	return "", errors.New("no Jira key given and the branch name has none; pass a key or run devflow start")
}

// Checklists returns the local acceptance-criteria store.
func (a *App) Checklists(ctx context.Context) contextx.Store {
	return contextx.Store{Dir: filepath.Join(a.Repo.GitDir(ctx), "devflow")}
}

// shellQuote preserves a single literal argument in a suggested POSIX shell command.
func shellQuote(arg string) string {
	return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
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
	Template    *TaskTemplateSnapshot `json:"template,omitempty"`
	revision    string                // observed file revision used to condition the approved write
	Key         string                `json:"key"`
	Event       string                `json:"event"`
	From        string                `json:"from"`
	To          string                `json:"to,omitempty"`
	Applied     bool                  `json:"applied"`
	Note        string                `json:"note,omitempty"`
	Status      provider.WriteStatus  `json:"status,omitempty"`
	URL         string                `json:"url,omitempty"`
	Branch      string                `json:"branch,omitempty"`
}

// PlanTransition works out what event would do to key without changing anything.
func (a *App) PlanTransition(ctx context.Context, key, event string) (TransitionResult, error) {
	if a.UsesTaskRefs() {
		return a.planTaskTransition(ctx, key, event, nil)
	}
	res := TransitionResult{Key: key, Event: event}
	ref, err := legacy.JiraRef("jira", key)
	if err != nil {
		return res, err
	}
	if _, err := a.jiraSite(); err != nil {
		return res, err
	}
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
	j, err := a.jiraTasks(nil)
	if err != nil {
		res.Note = "Jira not reachable: " + err.Error()
		return res, nil
	}
	task, err := j.GetFields(ctx, ref)
	if err != nil {
		return res, err
	}
	res.From = task.State.Name
	switch {
	case fm.Backwards(projectOf(key), res.From, target):
		res.Note = fmt.Sprintf("%s is before %s in the project's order; devflow only moves forward", target, res.From)
		res.To = ""
	case strings.EqualFold(res.From, target):
		res.Note = "already in " + target
		res.To = ""
	case task.State.Terminal:
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
	if a.usesFileTasks() {
		return plan, errors.New("file task transitions require a gated publication")
	}
	if a.usesAsana() {
		return a.applyAsanaTransition(ctx, plan)
	}
	if a.usesIssues() {
		return a.applyIssueTransition(ctx, plan)
	}
	ref, err := legacy.JiraRef("jira", plan.Key)
	if err != nil {
		return plan, err
	}
	fm, err := a.FlowMap(ctx)
	if err != nil {
		return plan, err
	}
	if target, ok := fm.Resolve(ref.Scope, plan.Event); !ok || target != plan.To {
		return plan, errors.New("transition destination differs from the Jira flow map")
	}
	j, err := a.jiraTasks(fm)
	if err != nil {
		return plan, err
	}
	result, err := j.Transition(ctx, ref, provider.TaskState{Name: plan.To}, provider.WriteOptions{})
	if errors.Is(err, legacy.ErrNoTransition) {
		plan.Note = fmt.Sprintf("no transition from %q to %q in this workflow", plan.From, plan.To)
		return plan, nil
	}
	plan.Applied = err == nil && result.Status == provider.Applied
	if result.Status == provider.AlreadyApplied {
		plan.Note = "already in " + plan.To
	}
	a.audit(policy.Remote, "transition", map[string]any{"site": a.jiraSettings().Site, "transition": plan}, err)
	return plan, err
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
	if a.usesFileTasks() {
		return a.transitionFileTask(ctx, plan, token)
	}
	var summary any = struct {
		TransitionResult
		Site string `json:"site"`
	}{plan, a.jiraSettings().Site}
	commands := []string{"devflow transition " + key + " " + shellQuote(event)}
	if a.usesIssues() || a.usesAsana() {
		ref, err := a.taskRef(ctx, plan.Key)
		if err != nil {
			return nil, err
		}
		summary = struct {
			TransitionResult
			TaskRef string `json:"task_ref"`
		}{plan, ref.String()}
		commands = nil
	}
	if p, err := a.gate(policy.Remote, "transition", summary, token, false, commands); p != nil || err != nil {
		return p, err
	}
	return a.ApplyTransition(ctx, plan)
}

// Comment posts a comment on a ticket.
func (a *App) Comment(ctx context.Context, key, text, token string) (any, error) {
	if a.usesFileTasks() || a.usesAsana() {
		return nil, provider.Unsupported(a.Cfg.Providers.TaskManagement.Provider, "comments")
	}
	if a.usesIssues() {
		return a.commentIssue(ctx, key, text, token)
	}
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
	body := text + viaNote(a)
	summary := map[string]string{"site": site, "key": key, "comment": body}
	if p, err := a.gate(policy.Remote, "comment", summary, token, false, nil); p != nil || err != nil {
		return p, err
	}
	j, err := a.jiraTasks(nil)
	if err != nil {
		return nil, err
	}
	_, err = j.Comment(ctx, ref, body, provider.WriteOptions{})
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
	if i := strings.LastIndexByte(key, '-'); i >= 0 {
		return key[:i]
	}
	return key
}
