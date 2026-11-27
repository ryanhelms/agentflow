package aisession

import (
	"context"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/boundary"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// ChainConfig is what the default chain decides with. Everything here is
// agentflow's own: the worktree of the starting process, the person's and
// the repository's configuration, the gate.
type ChainConfig struct {
	Session ID
	// AgentSessionID is the ACP sessionId the harness was given.
	AgentSessionID string
	Worktree       string // the worktree root
	// Ceiling is CeilingConfirm for a session an agent started.
	Ceiling string
	User    *config.User
	// AccountRules are the deny and ask rules of the account's own
	// agentflow home when AGENTFLOW_HOME is another folder: checked before
	// User's rules, so no rule there can allow what they deny or ask.
	AccountRules map[string]config.AIRule
	Repo         *config.Repo
	Gate         *policy.Gate
	// Never-list inputs.
	Protected        []string
	Branch           string
	CoordinatorMerge bool
	MergeBase        func(cmd string) (string, error)
	// RepoHome is the session's repository for boundary.Checker; Resolve
	// finds a folder's repository (boundary.GitResolver).
	RepoHome boundary.Repo
	Resolve  boundary.Resolver
	// Folders the session never-list protects (agentflow_home): the
	// agentflow home, the console registry (~/.agentflow-ui) and the
	// session store. Expand holds the values $HOME and $AGENTFLOW_HOME
	// expand to (the parent's real values and the account home).
	Protect []string
	Expand  map[string][]string
	// PluginDeny runs the enabled plugins' permission rules (deny only).
	PluginDeny func(ctx context.Context, class, action, branch string) error
	// InTest: in a test binary, terminal programs outside os.TempDir() are
	// denied test_guard.
	InTest bool
	// Guard is the test guard (harness.GuardPath).
	Guard func(string) error
}

// chainState is what the links leave for the later ones: why the request
// would ask, and whether a person's rule allows it. It travels in the
// context so the contract's Request stays as the ADR has it.
type chainState struct {
	ask         bool   // a rule or a repository list says ask
	askHandler  string // the link that said ask
	personAllow string // the allowing person rule's name
	env         []acp.EnvVariable
}

type chainKey struct{}

func stateOf(ctx context.Context) *chainState {
	if s, ok := ctx.Value(chainKey{}).(*chainState); ok {
		return s
	}
	return &chainState{}
}

// withTerminalEnv carries the agent's env entries of a terminal/create to
// the never-list, which checks their names and values. Values are never
// stored.
func withTerminalEnv(ctx context.Context, env []acp.EnvVariable) context.Context {
	s := stateOf(ctx)
	s.env = env
	return context.WithValue(ctx, chainKey{}, s)
}

// DefaultChain is the ADR's chain: never-list, repository boundary, policy
// rule, policy.Gate, default deny.
func DefaultChain(c ChainConfig) []PermissionHandler {
	w, _ := newWorktree(c.Worktree)
	return []PermissionHandler{
		&neverLink{c: c, w: w},
		&boundaryLink{c: c, w: w},
		&ruleLink{c: c, w: w},
		&gateLink{c: c},
		defaultLink{},
	}
}

// Run sends r through the chain. A request whose session is not this one
// is denied wrong_session before any link.
func Run(ctx context.Context, chain []PermissionHandler, r *Request) Verdict {
	ctx = context.WithValue(ctx, chainKey{}, &chainState{env: stateOf(ctx).env})
	var step func(i int) Next
	step = func(i int) Next {
		return func(ctx context.Context, r *Request) Verdict {
			if i >= len(chain) {
				return Verdict{Decision: Deny, Handler: HandlerDefault, Code: VerdictDefault}
			}
			v := chain[i].Decide(ctx, r, step(i+1))
			// Only the rule and gate links may allow or ask; any other
			// answer from another link is a deny.
			switch {
			case v.Decision == Allow && v.Handler != HandlerRule && v.Handler != HandlerGate,
				v.Decision == Ask && v.Handler != HandlerRule && v.Handler != HandlerGate && v.Handler != HandlerNever,
				v.Decision != Allow && v.Decision != Ask && v.Decision != Deny:
				return Verdict{Decision: Deny, Handler: HandlerDefault, Code: VerdictDefault}
			}
			return v
		}
	}
	return step(0)(ctx, r)
}

func deny(handler, code string) Verdict { return Verdict{Decision: Deny, Handler: handler, Code: code} }

// actionOf is the gate action and class of a request ("Per request kind").
func actionOf(r *Request) (policy.Class, string) {
	switch r.Kind {
	case KindRead:
		return policy.Read, ActionFSRead
	case KindWrite:
		return policy.Local, ActionFSWrite
	case KindTerminal:
		return policy.Local, ActionTerminal
	}
	switch r.ToolKind {
	case acp.ToolRead, acp.ToolSearch, acp.ToolThink:
		return policy.Read, ActionFSRead
	case acp.ToolEdit, acp.ToolDelete, acp.ToolMove:
		return policy.Local, ActionFSWrite
	case acp.ToolExecute:
		return policy.Local, ActionTerminal
	case acp.ToolFetch:
		return policy.Remote, ActionFetch
	}
	return policy.Local, ActionPermission
}

// ---- 1. never-list ----

type neverLink struct {
	c ChainConfig
	w worktree
}

func (l *neverLink) Decide(ctx context.Context, r *Request, next Next) Verdict {
	switch r.Kind {
	case KindRead:
		l.mark(r, IsSecretFile, ReasonSecretFile)
	case KindWrite:
		l.mark(r, IsControlFile, ReasonControlFile)
	case KindTerminal:
		if v, ok := l.terminal(ctx, r); ok {
			return v
		}
	case KindPermission:
		switch r.ToolKind {
		case acp.ToolEdit, acp.ToolDelete, acp.ToolMove:
			l.mark(r, IsControlFile, ReasonControlFile)
		case acp.ToolRead, acp.ToolSearch:
			l.mark(r, IsSecretFile, ReasonSecretFile)
		case acp.ToolExecute:
			r.PersonOnly, r.Reason = true, ReasonTerminal
		case acp.ToolThink, acp.ToolFetch:
		default: // switch_mode, other, unknown
			r.PersonOnly, r.Reason = true, ReasonKind
		}
	default:
		return deny(HandlerNever, VerdictDefault)
	}
	return next(ctx, r)
}

// mark makes r person-only when any form of any of its paths (as spelled
// or as resolved; "Phase 7 file access") is on the list, or, on macOS and
// Windows, has a non-ASCII component. The chain never trusts a field it
// did not set, so the forms are worked out again here.
func (l *neverLink) mark(r *Request, list func(string) bool, reason string) {
	for _, p := range r.Paths {
		forms, _ := l.w.forms(p)
		for _, f := range forms {
			if list(f) {
				r.PersonOnly, r.Reason = true, reason
				return
			}
		}
	}
	if foldCase() {
		for _, p := range r.Paths {
			forms, _ := l.w.forms(p)
			for _, f := range forms {
				if nonASCII(f) {
					r.PersonOnly, r.Reason = true, ReasonUnicodeName
					return
				}
			}
		}
	}
}

// ---- 2. repository boundary ----

type boundaryLink struct {
	c ChainConfig
	w worktree
}

func (l *boundaryLink) Decide(ctx context.Context, r *Request, next Next) Verdict {
	if l.w.real == "" {
		return deny(HandlerBoundary, VerdictOutsideWorktree)
	}
	switch r.Kind {
	case KindRead, KindWrite:
		if len(r.Paths) != 1 {
			return deny(HandlerBoundary, VerdictRelative)
		}
		if _, code := l.w.resolve(r.Paths[0].Abs); code != "" {
			return deny(HandlerBoundary, code)
		}
	case KindPermission:
		for _, p := range r.Paths {
			if _, code := l.w.resolve(p.Abs); code != "" && code != VerdictNotRegular {
				return deny(HandlerBoundary, code)
			}
		}
	case KindTerminal:
		cmd := r.Command
		if cmd == nil {
			return deny(HandlerBoundary, VerdictProgram)
		}
		cwd := cmd.Cwd.Abs
		if rel, ok := relOf(l.w.real, cwd); !ok || rel != "" {
			if _, code := l.w.resolve(cwd); code != "" && code != VerdictNotRegular {
				return deny(HandlerBoundary, code)
			}
		}
		if _, inside := relOf(l.w.real, cmd.Resolved); inside {
			if _, code := l.w.resolve(cmd.Resolved); code != "" {
				return deny(HandlerBoundary, code)
			}
		}
		if l.c.RepoHome.CommonDir != "" && l.c.Resolve != nil {
			ch := &boundary.Checker{Home: l.c.RepoHome, Cwd: cwd, Resolve: l.c.Resolve}
			if ref := ch.Check(cmd.Line); ref != nil {
				return deny(HandlerBoundary, VerdictOtherRepository)
			}
		}
	}
	return next(ctx, r)
}

// ---- 3. policy rule ----

type ruleLink struct {
	c ChainConfig
	w worktree
}

func (l *ruleLink) Decide(ctx context.Context, r *Request, next Next) Verdict {
	st := stateOf(ctx)
	paths := pathForms(l.w, r)
	var rels []string
	for _, fs := range paths {
		rels = append(rels, fs...)
	}
	// The repository's lists: ask or deny only. Any form of any path.
	if l.c.Repo != nil {
		for _, g := range l.c.Repo.AI.Permissions.Deny {
			if anyMatch(g, rels) {
				return deny(HandlerRule, "rule:repo")
			}
		}
		for _, g := range l.c.Repo.AI.Permissions.Ask {
			if anyMatch(g, rels) {
				st.ask, st.askHandler = true, HandlerRule
				if r.Reason == "" {
					r.Reason = ReasonRule
				}
			}
		}
	}
	// A terminal that carries the agent's own environment is never
	// allowed by a rule: a value can run code whatever the program
	// (GIT_EXTERNAL_DIFF, PAGER, GOFLAGS). A person sees each value.
	if r.Kind == KindTerminal && len(st.env) > 0 {
		st.ask, st.askHandler = true, HandlerRule
		if r.Reason == "" || r.Reason == ReasonTerminal {
			r.Reason = ReasonEnv
		}
	}
	// The account layer's deny and ask rules (account.go): any match.
	for _, name := range slices.Sorted(maps.Keys(l.c.AccountRules)) {
		rule := l.c.AccountRules[name]
		if config.ValidateAIRule(name, rule) != nil || !ruleMatches(rule, r, paths) {
			continue
		}
		switch rule.Decision {
		case config.AIDecisionDeny:
			return deny(HandlerRule, "rule:account:"+name)
		case config.AIDecisionAsk:
			st.ask, st.askHandler = true, HandlerRule
			if r.Reason == "" {
				r.Reason = ReasonRule
			}
		}
	}
	// The person's rules, by name; the first that matches decides. When
	// something above already asks, only a deny still changes the answer.
	if l.c.User != nil {
		rules := l.c.User.AI.Permissions.Rules
		for _, name := range slices.Sorted(maps.Keys(rules)) {
			rule := rules[name]
			if config.ValidateAIRule(name, rule) != nil || !ruleMatches(rule, r, paths) {
				continue
			}
			switch rule.Decision {
			case config.AIDecisionDeny:
				return deny(HandlerRule, "rule:"+name)
			case config.AIDecisionAsk:
				st.ask, st.askHandler = true, HandlerRule
				if r.Reason == "" {
					r.Reason = ReasonRule
				}
			case config.AIDecisionAllow:
				if !st.ask {
					st.personAllow = name
				}
			}
			break
		}
	}
	// The enabled plugins' permission rules: deny only.
	if l.c.PluginDeny != nil {
		class, action := actionOf(r)
		if err := l.c.PluginDeny(ctx, string(class), action, l.c.Branch); err != nil {
			return deny(HandlerRule, VerdictPlugin)
		}
	}
	return next(ctx, r)
}

// pathForms is, for each path of r (and a terminal's cwd), its forms: as
// spelled and as resolved. A path outside the worktree has none.
func pathForms(w worktree, r *Request) [][]string {
	var out [][]string
	add := func(p Path) {
		if forms, outside := w.forms(p); !outside && len(forms) > 0 && forms[0] != "" {
			out = append(out, forms)
		}
	}
	for _, p := range r.Paths {
		add(p)
	}
	if r.Command != nil && r.Command.Cwd.Abs != "" {
		add(r.Command.Cwd)
	}
	return out
}

func anyMatch(glob string, rels []string) bool {
	for _, rel := range rels {
		if MatchGlob(glob, rel, foldCase()) {
			return true
		}
	}
	return false
}

// ruleMatches: fs rules match the paths of a read or write (a permission
// request of the same tool kind included), a terminal rule the resolved
// program's base name, a tool rule the tool kind. Strictest wins: a deny
// or ask rule matches when any form of any path matches; an allow rule
// only when every form of every path does, so a link cannot carry an
// allow for docs/** onto .github/workflows.
func ruleMatches(rule config.AIRule, r *Request, paths [][]string) bool {
	match := func() bool {
		if len(r.Paths) == 0 || len(paths) != len(r.Paths) {
			return false
		}
		if rule.Decision != config.AIDecisionAllow {
			for _, forms := range paths {
				if anyMatch(rule.Match, forms) {
					return true
				}
			}
			return false
		}
		for _, forms := range paths {
			for _, f := range forms {
				if !MatchGlob(rule.Match, f, foldCase()) {
					return false
				}
			}
		}
		return true
	}
	_, action := actionOf(r)
	switch rule.Kind {
	case config.AIRuleKindFSRead:
		return action == ActionFSRead && match()
	case config.AIRuleKindFSWrite:
		return action == ActionFSWrite && match()
	case config.AIRuleKindTerminal:
		if r.Kind != KindTerminal || r.Command == nil {
			return false
		}
		name := programBase(r.Command.Resolved)
		ok, _ := matchName(rule.Match, name)
		return ok
	case config.AIRuleKindTool:
		return r.Kind == KindPermission && rule.Match == string(r.ToolKind)
	}
	return false
}

func programBase(p string) string {
	return boundary.ProgramName(p)
}

func matchName(pattern, name string) (bool, error) {
	if foldCase() {
		pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	}
	return path.Match(pattern, name)
}

// ---- 4. policy.Gate, and combining ----

type gateLink struct{ c ChainConfig }

func (l *gateLink) Decide(ctx context.Context, r *Request, next Next) Verdict {
	st := stateOf(ctx)
	class, action := actionOf(r)
	if l.c.Gate == nil {
		return next(ctx, r)
	}
	g := l.c.Gate
	ceiling := l.c.Ceiling == CeilingConfirm && action != ActionFSRead
	if ceiling {
		g = g.WithCeiling(config.Confirm)
	}
	k, _ := config.DescribeAutonomyAction(action)
	d := g.Explain(class, action, nil, k.Mandatory)
	var gate Verdict
	switch d.Level {
	case config.Auto:
		gate = Verdict{Decision: Allow, Handler: HandlerGate, Code: VerdictGateAuto}
	case config.Confirm:
		gate = Verdict{Decision: Ask, Handler: HandlerGate, Code: VerdictGateConfirm}
	case config.Suggest:
		return deny(HandlerGate, VerdictGateSuggest)
	default:
		return deny(HandlerGate, VerdictGateNever)
	}
	// Any deny has won by now. Person-only, the ceiling or a rule's ask
	// asks; then a person's allow rule allows; then the gate answers.
	switch {
	case r.PersonOnly:
		return Verdict{Decision: Ask, Handler: HandlerNever, Code: r.Reason}
	case ceiling:
		if r.Reason == "" {
			r.Reason = ReasonCeiling
		}
		return Verdict{Decision: Ask, Handler: HandlerGate, Code: ReasonCeiling}
	case st.ask:
		return Verdict{Decision: Ask, Handler: st.askHandler, Code: r.Reason}
	case st.personAllow != "":
		return Verdict{Decision: Allow, Handler: HandlerRule, Code: "rule:" + st.personAllow}
	}
	if needsLocations(r) {
		r.Reason = ReasonNoLocations
		return Verdict{Decision: Ask, Handler: HandlerGate, Code: ReasonNoLocations}
	}
	if gate.Decision == Ask {
		switch {
		case r.Reason != "":
		case r.Kind == KindTerminal:
			r.Reason = ReasonTerminal
		default:
			r.Reason = ReasonGate
		}
		gate.Code = r.Reason
	}
	return gate
}

// needsLocations: a permission request for a read, search, edit, delete or
// move with no locations asks a person.
func needsLocations(r *Request) bool {
	if r.Kind != KindPermission || len(r.Paths) > 0 {
		return false
	}
	switch r.ToolKind {
	case acp.ToolRead, acp.ToolSearch, acp.ToolEdit, acp.ToolDelete, acp.ToolMove:
		return true
	}
	return false
}

// ---- 5. default ----

type defaultLink struct{}

func (defaultLink) Decide(context.Context, *Request, Next) Verdict {
	return deny(HandlerDefault, VerdictDefault)
}
