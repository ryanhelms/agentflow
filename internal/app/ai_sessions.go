package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// AI sessions (ADR, "Phase 7 commands"): ai run, status, respond and cancel.
// These methods are thin: the rules live in aisession.Manager, and the
// aisession.Options come from the caller (internal/command/ops), so this
// package never imports internal/command. The requester is on ctx
// (aisession.WithRequester), set by the calling surface; without one every
// rule treats the caller as an agent.

// NewAIManager builds the session Manager from its Options. Production
// sets it to aisession.NewManager; tests set a fake. While it is nil every
// session operation fails with ErrNoAISessions.
var NewAIManager func(aisession.Options) aisession.Manager

// ErrNoAISessions: this build cannot run AI sessions.
var ErrNoAISessions = errors.New("AI sessions are not available in this build of agentflow")

// AIRefusal shows an aisession.Refusal with its code and fix, so both reach
// the CLI's error line and the tool's error text. errors.As still finds the
// *aisession.Refusal.
type AIRefusal struct{ Refusal *aisession.Refusal }

func (e *AIRefusal) Error() string {
	msg := e.Refusal.Message + " (" + e.Refusal.Code + ")"
	if e.Refusal.Fix != "" {
		msg += ". " + e.Refusal.Fix
	}
	return msg
}

func (e *AIRefusal) Unwrap() error { return e.Refusal }

// refused wraps a *aisession.Refusal in err as an AIRefusal.
func refused(err error) error {
	var r *aisession.Refusal
	var done *AIRefusal
	if err == nil || errors.As(err, &done) || !errors.As(err, &r) {
		return err
	}
	return &AIRefusal{Refusal: r}
}

func aiManager(o aisession.Options) (aisession.Manager, error) {
	if NewAIManager == nil {
		return nil, ErrNoAISessions
	}
	return NewAIManager(o), nil
}

// AIRunRequest is one ai run. There is no directory field: the session runs
// in this App's worktree, which is the starting process's own.
type AIRunRequest struct {
	Harness string
	Prompt  string
	Detach  bool
}

// AIRunResult is what ai run returns once the session record is written.
type AIRunResult struct {
	Session  aisession.ID    `json:"session"`
	State    aisession.State `json:"state"`
	Harness  string          `json:"harness"`
	Worktree string          `json:"worktree"`
	Detached bool            `json:"detached"`
	// Next is the command to follow the session.
	Next string `json:"next"`
}

// inSession reports whether ctx's requester, the App's client or the
// process environment says the caller is an AI session.
func (a *App) inSession(ctx context.Context) bool {
	r, ok := aisession.RequesterFrom(ctx)
	return ok && r.Kind == aisession.RequesterSession || policy.IsSessionClient(a.Client) || os.Getenv(SessionEnv) != ""
}

// nested is the refusal for a session that asks to start a session. The
// Manager refuses it too; this check keeps it out of the Manager's hands.
func nested() error {
	return &AIRefusal{Refusal: &aisession.Refusal{Code: aisession.RefuseNested,
		Message: "an AI session cannot start an AI session",
		Fix:     "Ask the person to run agentflow ai run in their own terminal."}}
}

// startRequest is r in this App's worktree.
func (a *App) startRequest(r AIRunRequest) aisession.StartRequest {
	return aisession.StartRequest{Harness: r.Harness, Prompt: r.Prompt, Dir: a.Repo.Dir, Detach: r.Detach}
}

// AIPlan runs every ai run check and returns the confirmation summary.
func (a *App) AIPlan(ctx context.Context, o aisession.Options, r AIRunRequest) (aisession.Plan, error) {
	if a.inSession(ctx) {
		return aisession.Plan{}, nested()
	}
	m, err := aiManager(o)
	if err != nil {
		return aisession.Plan{}, err
	}
	p, err := m.Prepare(ctx, a.startRequest(r))
	return p, refused(err)
}

// AIRun starts the session: the Manager checks again, writes the record
// and, when detached, starts the detached host. A foreground caller then
// calls AIHost.
func (a *App) AIRun(ctx context.Context, o aisession.Options, r AIRunRequest) (AIRunResult, error) {
	if a.inSession(ctx) {
		return AIRunResult{}, nested()
	}
	m, err := aiManager(o)
	if err != nil {
		return AIRunResult{}, err
	}
	s, err := m.Start(ctx, a.startRequest(r))
	if err != nil {
		return AIRunResult{}, refused(err)
	}
	detached := s.Host.Mode != aisession.HostForeground
	return AIRunResult{Session: s.ID, State: s.State, Harness: s.Harness, Worktree: tildePath(s.Worktree), Detached: detached,
		Next: "agentflow ai status " + string(s.ID) + " --follow"}, nil
}

// AIHost claims a starting session and runs it until it ends.
func (a *App) AIHost(ctx context.Context, o aisession.Options, id aisession.ID, h aisession.HostOptions) (aisession.Session, error) {
	m, err := aiManager(o)
	if err != nil {
		return aisession.Session{}, err
	}
	s, err := m.Host(ctx, id, h)
	return s, refused(err)
}

// AISessionStatus is one session with its waiting asks.
type AISessionStatus struct {
	aisession.Session
	Asks []AIAsk `json:"asks,omitempty"`
}

// AIAsk is a waiting ask as ai status and the tools show it: every field of
// aisession.AskRecord except the file text of a write, which only the person
// answering sees (the console, the foreground prompt, ai respond --allow).
type AIAsk struct {
	ID         string                 `json:"id"`
	Session    aisession.ID           `json:"session"`
	Kind       aisession.Kind         `json:"kind"`
	ToolCallID string                 `json:"tool_call,omitempty"`
	ToolKind   acp.ToolKind           `json:"tool_kind,omitempty"`
	Title      string                 `json:"title,omitempty"`
	Options    []acp.PermissionOption `json:"options,omitempty"`
	Paths      []string               `json:"paths,omitempty"`
	Line       string                 `json:"line,omitempty"`
	Cwd        string                 `json:"cwd,omitempty"`
	EnvNames   []string               `json:"env_names,omitempty"`
	// Program is the resolved program a terminal ask would start, and
	// ResolvedPaths where each path leads through a link. The env values
	// are not here: only the person answering sees them.
	Program       string    `json:"program,omitempty"`
	ResolvedPaths []string  `json:"resolved_paths,omitempty"`
	Size          int64     `json:"size,omitempty"`
	Reason        string    `json:"reason"`
	CanAllow      bool      `json:"can_allow"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	Fingerprint   string    `json:"fingerprint"`
}

func askViews(asks []aisession.AskRecord) []AIAsk {
	if len(asks) == 0 {
		return nil
	}
	out := make([]AIAsk, len(asks))
	for i, r := range asks {
		out[i] = AIAsk{ID: r.ID, Session: r.Session, Kind: r.Kind, ToolCallID: r.ToolCallID, ToolKind: r.ToolKind, Title: r.Title,
			Options: r.Options, Paths: r.Paths, Line: r.Line, Cwd: r.Cwd, EnvNames: r.EnvNames, Program: r.Program, ResolvedPaths: r.ResolvedPaths, Size: r.Size, Reason: r.Reason,
			CanAllow: r.CanAllow, CreatedAt: r.CreatedAt, ExpiresAt: r.ExpiresAt, Fingerprint: r.Fingerprint}
	}
	return out
}

// AIStatusList is ai status without a session: every session, newest first.
type AIStatusList struct {
	Sessions []AISessionStatus `json:"sessions"`
}

// AISessionItems is ai status SESSION: the session, its waiting asks and
// the transcript items after since.
type AISessionItems struct {
	Session AISessionStatus  `json:"session"`
	Items   []aisession.Item `json:"items"`
	NextSeq int64            `json:"next_seq"`
}

// MaxStatusItems caps the items one ai status call returns.
const MaxStatusItems = 500

// aiScope limits ctx to this App's repository: ai status, respond and
// cancel see and act only on the sessions of the caller's repository (all
// its worktrees). Outside a repository they refuse.
func (a *App) aiScope(ctx context.Context) (context.Context, error) {
	if a.Repo == nil || a.Repo.Dir == "" {
		return nil, notInRepository()
	}
	common, err := a.commonDir(ctx)
	if err != nil || common == "" {
		return nil, notInRepository()
	}
	return aisession.WithScope(ctx, common), nil
}

func notInRepository() error {
	return &AIRefusal{Refusal: &aisession.Refusal{Code: aisession.RefuseNotWorktree,
		Message: "AI sessions are listed and answered per repository, and this directory is not in one",
		Fix:     "Run the command inside the repository (any of its worktrees) whose session you mean."}}
}

// AIStatus lists the sessions, or one session's items after since.
func (a *App) AIStatus(ctx context.Context, o aisession.Options, id aisession.ID, since int64) (any, error) {
	ctx, err := a.aiScope(ctx)
	if err != nil {
		return nil, err
	}
	m, err := aiManager(o)
	if err != nil {
		return nil, err
	}
	if id == "" {
		list, err := m.List(ctx)
		if err != nil {
			return nil, err
		}
		out := AIStatusList{Sessions: make([]AISessionStatus, 0, len(list))}
		for _, s := range list {
			st := AISessionStatus{Session: s}
			if s.State == aisession.StateWaiting {
				if _, asks, err := m.Get(ctx, s.ID); err == nil {
					st.Asks = askViews(asks)
				}
			}
			out.Sessions = append(out.Sessions, st)
		}
		return out, nil
	}
	s, asks, err := m.Get(ctx, id)
	if err != nil {
		return nil, refused(err)
	}
	items, next, err := m.Items(ctx, id, since, MaxStatusItems)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []aisession.Item{}
	}
	return AISessionItems{Session: AISessionStatus{Session: s, Asks: askViews(asks)}, Items: items, NextSeq: next}, nil
}

// AISession returns one session and its waiting asks.
func (a *App) AISession(ctx context.Context, o aisession.Options, id aisession.ID) (aisession.Session, []aisession.AskRecord, error) {
	ctx, err := a.aiScope(ctx)
	if err != nil {
		return aisession.Session{}, nil, err
	}
	m, err := aiManager(o)
	if err != nil {
		return aisession.Session{}, nil, err
	}
	s, asks, err := m.Get(ctx, id)
	return s, asks, refused(err)
}

// AIRespond answers an ask. The answer rules (who may allow) are the
// Manager's; a session never answers.
func (a *App) AIRespond(ctx context.Context, o aisession.Options, r aisession.AnswerRequest) (aisession.AnswerResult, error) {
	if a.inSession(ctx) {
		return aisession.AnswerResult{}, &AIRefusal{Refusal: &aisession.Refusal{Code: aisession.AnswerSelf, Message: "an AI session cannot answer requests"}}
	}
	ctx, err := a.aiScope(ctx)
	if err != nil {
		return aisession.AnswerResult{}, err
	}
	m, err := aiManager(o)
	if err != nil {
		return aisession.AnswerResult{}, err
	}
	res, err := m.Answer(ctx, r)
	return res, refused(err)
}

// AICancelResult is what ai cancel returns.
type AICancelResult struct {
	Session aisession.ID    `json:"session"`
	State   aisession.State `json:"state"`
}

// AICancelPlan is the confirmation summary of ai cancel.
func (a *App) AICancelPlan(ctx context.Context, o aisession.Options, id aisession.ID) (map[string]any, error) {
	if a.inSession(ctx) {
		return nil, &AIRefusal{Refusal: &aisession.Refusal{Code: aisession.RefuseNested, Message: "an AI session cannot cancel a session"}}
	}
	s, _, err := a.AISession(ctx, o, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": s.ID, "harness": s.Harness, "state": s.State, "worktree": tildePath(s.Worktree)}, nil
}

// AICancel asks the session's host to cancel it.
func (a *App) AICancel(ctx context.Context, o aisession.Options, id aisession.ID) (AICancelResult, error) {
	if a.inSession(ctx) {
		return AICancelResult{}, &AIRefusal{Refusal: &aisession.Refusal{Code: aisession.RefuseNested, Message: "an AI session cannot cancel a session"}}
	}
	ctx, err := a.aiScope(ctx)
	if err != nil {
		return AICancelResult{}, err
	}
	m, err := aiManager(o)
	if err != nil {
		return AICancelResult{}, err
	}
	s, err := m.Cancel(ctx, id)
	if err != nil {
		return AICancelResult{}, refused(err)
	}
	return AICancelResult{Session: s.ID, State: s.State}, nil
}

// tildePath shows a path under the OS home as "~/...".
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}
