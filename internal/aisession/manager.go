package aisession

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// manager is the production Manager.
type manager struct {
	o  Options
	mu sync.Mutex // guards st: concurrent calls share one store
	st *fileStore
}

// NewManager returns the Manager over Options.Home's session store.
func NewManager(o Options) Manager {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &manager{o: o}
}

func (m *manager) store() (*fileStore, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.st != nil {
		return m.st, nil
	}
	st, err := openStore(m.o.Home, m.o.Now)
	if err != nil {
		return nil, err
	}
	m.st = st
	return st, nil
}

// requester is the caller: without one, an agent.
func requester(ctx context.Context) Requester {
	r, ok := RequesterFrom(ctx)
	if !ok || r.Kind == "" {
		return Requester{Kind: RequesterAgent}
	}
	return r
}

// selfOrSession reports whether the caller is a session, its MCP server or
// a process below a session.
func selfOrSession(r Requester) bool {
	return r.Kind == RequesterSession || policy.IsSessionClient(r.Client) || os.Getenv(EnvSession) != ""
}

// prepared is what Prepare worked out, for Start and the host.
type prepared struct {
	plan    Plan
	req     Requester
	user    *config.User
	repo    *config.Repo
	root    string // the worktree root, resolved
	common  string // the common git directory
	branch  string
	found   harness.Found
	harness harness.Harness
	limits  Limits
}

// errWorkDirs: Options.WorkDirs failed, so the served repositories cannot
// be told.
var errWorkDirs = errors.New("aisession: the repositories the console serves cannot be read")

// workDirs is root and every existing absolute directory Options.WorkDirs
// lists. A directory that no longer exists holds no program, so it is
// skipped rather than making every PATH entry count as inside a
// repository.
func (o Options) workDirs(ctx context.Context, root string) ([]string, error) {
	dirs := []string{root}
	if o.WorkDirs == nil {
		return dirs, nil
	}
	served, err := o.WorkDirs(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errWorkDirs, err)
	}
	for _, d := range served {
		if d == "" || !filepath.IsAbs(d) || slices.Contains(dirs, d) {
			continue
		}
		if _, err := os.Stat(d); err == nil {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

func refuse(code, msg, fix string) error { return &Refusal{Code: code, Message: msg, Fix: fix} }

// worktreeOf finds the worktree that contains dir and its git directories.
func worktreeOf(ctx context.Context, dir string) (root, gitDir, common, branch string, err error) {
	r, err := gitx.Open(ctx, dir)
	if err != nil {
		return "", "", "", "", err
	}
	out, err := r.Git(ctx, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	if err != nil {
		return "", "", "", "", err
	}
	lines := strings.Fields(out)
	if len(lines) != 2 {
		return "", "", "", "", errors.New("aisession: git rev-parse gave no directories")
	}
	root = resolvedPath(r.Dir)
	return root, resolvedPath(lines[0]), resolvedPath(lines[1]), r.Branch(ctx), nil
}

func (m *manager) Prepare(ctx context.Context, r StartRequest) (Plan, error) {
	p, err := m.prepare(ctx, r)
	if err != nil {
		return Plan{}, err
	}
	return p.plan, nil
}

func (m *manager) prepare(ctx context.Context, r StartRequest) (*prepared, error) {
	req := requester(ctx)
	if selfOrSession(req) {
		return nil, refuse(RefuseNested, "A session cannot start a session.", "")
	}
	if req.Kind != RequesterPerson && req.Kind != RequesterAgent {
		return nil, refuse(RefuseNested, "Only a person or an agent may start a session.", "")
	}
	home := m.o.Home
	if home == "" || !filepath.IsAbs(home) {
		return nil, refuse(RefuseHomeUnsafe, "The agentflow home is not an absolute path.", "")
	}
	if inside, why := config.InsideRepository(home); inside || harness.HomeUnsafe(home) {
		if why == "" {
			why = "is not owned by you or is writable by others"
		}
		return nil, refuse(RefuseHomeUnsafe, "The agentflow home "+why+", so agentflow does not start sessions.", "")
	}
	user, err := config.LoadUserAt(home)
	if err != nil {
		return nil, refuse(RefuseHomeUnsafe, "Your config.yml does not load, so agentflow does not start sessions: "+err.Error()+".", "Fix config.yml, then try again.")
	}
	sess := user.AI.Sessions
	if req.Kind == RequesterAgent && !sess.AgentStartOn() {
		return nil, refuse(RefuseAgentStartOff, "You turned off letting agents start sessions.", "Turn on \"Let agents start sessions\" in Settings, or run agentflow ai run in your own terminal.")
	}
	if r.Dir == "" || !filepath.IsAbs(r.Dir) {
		return nil, refuse(RefuseNotWorktree, "A session needs the starting process's own directory.", "")
	}
	root, gitDir, common, branch, err := worktreeOf(ctx, r.Dir)
	if err != nil {
		return nil, refuse(RefuseNotWorktree, "This directory is not in a git worktree.", "Run agentflow start <task> --worktree, then start the session there.")
	}
	repo, _, err := config.LoadRepoCore(root)
	if err != nil || repo == nil {
		repo = &config.Repo{}
	}
	if repo.AI.Sessions.Disabled {
		return nil, refuse(RefuseRepoDisabled, "This repository turned sessions off (ai.sessions.disabled).", "")
	}
	if gitDir == common {
		return nil, refuse(RefusePrimaryCheckout, "Sessions run only in a task worktree, never in the primary checkout.", "Run agentflow start <task> --worktree and start the session in the worktree it prints.")
	}
	workDirs, err := m.o.workDirs(ctx, root)
	if err != nil {
		return nil, refuse(RefuseUnsafeEnvironment, "agentflow cannot read which repositories the console serves, so it cannot keep their programs off the session PATH.",
			"Check that ~/.agentflow-ui is a private folder you own, or stop the console with agentflow ui --stop.")
	}
	found, h, err := harness.Locate(ctx, home, m.o.Version, func() []string { return workDirs }, r.Harness)
	switch {
	case errors.Is(err, harness.ErrTestGuard):
		return nil, refuse(RefuseTestGuard, "In a test binary, the harness is outside the test's temporary directory.", "")
	case errors.Is(err, harness.ErrNotFound):
		return nil, refuse(RefuseNotReady, fmt.Sprintf("No enabled harness is called %q.", harnessText(r.Harness)), "Run agentflow ai harnesses to see the harnesses agentflow found.")
	case err != nil:
		fix := "Run agentflow ai harnesses --probe --id " + harnessText(r.Harness) + "."
		if h.Fix != nil && h.Fix.Text != "" {
			fix = h.Fix.Text
			if h.Fix.Command != "" {
				fix += " " + h.Fix.Command
			}
		}
		return nil, refuse(RefuseNotReady, fmt.Sprintf("The harness %s is not ready for a session (%s).", harnessText(r.Harness), h.State), fix)
	}
	if req.Kind != RequesterPerson && slices.Contains(h.Notes, harness.NotePluginLaunch) {
		return nil, refuse(RefusePluginLaunch, "A plugin chose what this harness starts, so only a person may start a session with it.", "Run agentflow ai run in your own terminal.")
	}
	if err := harness.GuardPath(found.Path); err != nil {
		return nil, refuse(RefuseTestGuard, "In a test binary, the harness is outside the test's temporary directory.", "")
	}
	if err := harness.GuardPath(found.Target); err != nil {
		return nil, refuse(RefuseTestGuard, "In a test binary, the harness is outside the test's temporary directory.", "")
	}
	accountHome, _ := config.AccountHome()
	// The environment is checked now with a provisional directory; the host
	// builds it again for the real session directory.
	env, ref := buildEnv(ctx, envInput{
		Parent: os.Environ(), AccountHome: accountHome, SessionDir: filepath.Join(home, storeDir, sessionsDir, "check"),
		HomeMode: sess.EffectiveHome(), HarnessDir: found.PathDir, Definition: found.Definition,
		WorkDirs: workDirs, Session: "check", Windows: isWindows(),
	})
	if ref != nil {
		return nil, ref
	}
	limits := Limits{
		MaxRunning: sess.EffectiveMaxRunning(), MaxDuration: sess.EffectiveMaxDuration(),
		IdleTimeout: sess.EffectiveIdleTimeout(), AskTimeout: sess.EffectiveAskTimeout(),
	}
	detached := r.Detach || req.Kind != RequesterPerson
	if detached && m.o.Detach == nil && testing.Testing() {
		return nil, refuse(RefuseTestGuard, "In a test binary, a detached session needs Options.Detach (aisessiontest.InProcessDetach).", "")
	}
	ceiling := ""
	if req.Kind != RequesterPerson {
		ceiling = CeilingConfirm
	}
	plan := Plan{
		Harness: found.ID, HarnessName: h.Name, Version: h.Version, Command: h.Command, Path: tilde(found.Path),
		Args: append([]string{}, found.Definition.Args...), Identity: found.Identity,
		Worktree: tilde(root), Branch: branch, StartedBy: req, Ceiling: ceiling, Home: sess.EffectiveHome(),
		EnvNames: env.Names, CredentialEnv: env.Credential, Limits: limits,
		Prompt: Sanitize(r.Prompt, MaxPrompt), Detached: detached,
	}
	if plan.Args == nil {
		plan.Args = []string{}
	}
	switch mcp := m.mcpServer("check", root, sess.AgentflowMCPOn()); {
	case mcp.skipped != "":
		plan.MCPSkipped = mcp.skipped
	case mcp.ok:
		plan.MCP = true
	}
	plan.ControlFiles = changedControlFiles(ctx, root)
	return &prepared{plan: plan, req: req, user: user, repo: repo, root: root, common: common, branch: branch, found: found, harness: h, limits: limits}, nil
}

func harnessText(id string) string { return SanitizeLine(id, 64) }

type mcpChoice struct {
	ok      bool
	skipped string
}

// mcpServer decides whether agentflow's MCP server is passed: on, an entry
// from Options.MCPServer, and a command outside the worktree.
func (m *manager) mcpServer(id ID, root string, on bool) mcpChoice {
	if !on {
		return mcpChoice{skipped: "off"}
	}
	if m.o.MCPServer == nil {
		return mcpChoice{}
	}
	e := m.o.MCPServer(id)
	if e == nil {
		return mcpChoice{}
	}
	if !filepath.IsAbs(e.Command) {
		return mcpChoice{skipped: "mcp_skipped"}
	}
	if _, inside := relOf(root, resolvedPath(e.Command)); inside {
		return mcpChoice{skipped: "mcp_skipped"}
	}
	return mcpChoice{ok: true}
}

// changedControlFiles lists the control files that differ from the merge
// base with the default branch, or are untracked.
func changedControlFiles(ctx context.Context, root string) []string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r := &gitx.Repo{Dir: root}
	var names []string
	add := func(out string) {
		for _, line := range strings.Split(out, "\x00") {
			if line = strings.TrimSpace(line); line != "" {
				names = append(names, line)
			}
		}
	}
	if base, err := r.Git(ctx, "merge-base", "HEAD", r.BaseRef(ctx)); err == nil {
		if out, err := r.Git(ctx, "diff", "--name-only", "-z", strings.TrimSpace(base)); err == nil {
			add(out)
		}
	}
	if out, err := r.Git(ctx, "ls-files", "--others", "--exclude-standard", "-z"); err == nil {
		add(out)
	}
	var out []string
	for _, n := range names {
		if IsControlFile(n) && !slices.Contains(out, n) {
			out = append(out, SanitizeLine(n, 512))
		}
	}
	slices.Sort(out)
	return out
}

func (m *manager) Start(ctx context.Context, r StartRequest) (Session, error) {
	p, err := m.prepare(ctx, r)
	if err != nil {
		return Session{}, err
	}
	st, err := m.store()
	if err != nil {
		return Session{}, err
	}
	_ = st.Prune(p.user.AI.Sessions.EffectiveKeepDays(), p.user.AI.Sessions.KeepTranscriptsOn())
	id := NewID()
	wtID := ""
	if m.o.WorktreeID != nil {
		wtID, _ = m.o.WorktreeID(ctx, p.root)
	}
	mode := HostForeground
	if p.plan.Detached {
		mode = HostDetached
	}
	sess := Session{
		ID: id, Harness: p.found.ID, HarnessName: SanitizeLine(p.harness.Name, 64), Identity: p.found.Identity,
		Repo: repoIDOf(p.common), Common: p.common, WorktreeID: wtID, Worktree: p.root, Branch: SanitizeLine(p.branch, 256),
		StartedBy: p.req, Ceiling: p.plan.Ceiling, Home: p.plan.Home, EnvNames: p.plan.EnvNames,
		MCP: p.plan.MCP, Host: HostInfo{Mode: mode}, CreatedAt: m.o.Now().UTC(),
	}
	created, err := st.Create(sess, r.Prompt, Caps{MaxRunning: p.limits.MaxRunning, PerWorktree: PerWorktree, StartsPerHour: StartsPerHour})
	if err != nil {
		return Session{}, err
	}
	ev := newEvents(m.o, created)
	ev.started(ctx, created)
	ev.changed(ctx, created, "")
	if p.plan.Detached {
		detach := m.o.Detach
		if detach == nil {
			detach = execDetach(st)
		}
		if err := detach(ctx, id); err != nil {
			ended, _ := st.Update(id, func(s *Session) error {
				s.State, s.Code = StateEnded, CodeLaunch
				return nil
			})
			ev.changed(ctx, ended, StateStarting)
			ev.ended(ctx, ended)
			return ended, fmt.Errorf("aisession: start the detached host: %w", err)
		}
	}
	return created, nil
}

// execDetach starts `agentflow ai host --session ID` from this binary, in
// a new session, with its output on host.log.
func execDetach(st *fileStore) func(ctx context.Context, id ID) error {
	return func(_ context.Context, id ID) error {
		if testing.Testing() {
			return errors.New("aisession: test guard: a test never re-executes its binary; set Options.Detach")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return err
		}
		log, err := os.OpenFile(filepath.Join(st.Dir(id), hostLogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer log.Close()
		devnull, err := os.Open(os.DevNull)
		if err != nil {
			return err
		}
		defer devnull.Close()
		cmd := &exec.Cmd{Path: exe, Args: []string{exe, "ai", "host", "--session", string(id)}, Env: os.Environ(), Stdin: devnull, Stdout: log, Stderr: log}
		detachAttr(cmd)
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
}

// repoIDOf reads the repository's bus repository ID from its event store
// (<common>/agentflow/events/repo-id), or "" when it has none yet.
func repoIDOf(common string) string {
	b, err := os.ReadFile(filepath.Join(common, "agentflow", "events", "repo-id"))
	if err != nil {
		return ""
	}
	id := strings.TrimSpace(string(b))
	if len(id) > 64 || strings.ContainsAny(id, "/\\\x00 ") {
		return ""
	}
	return id
}

func (m *manager) Host(ctx context.Context, id ID, o HostOptions) (Session, error) {
	st, err := m.store()
	if err != nil {
		return Session{}, err
	}
	h, err := newHost(m, st, id, o)
	if err != nil {
		return Session{}, err
	}
	return h.Run(ctx)
}

func (m *manager) List(ctx context.Context) ([]Session, error) {
	st, err := m.store()
	if err != nil {
		return nil, err
	}
	all, err := st.List()
	if err != nil {
		return nil, err
	}
	scope, scoped := ScopeFrom(ctx)
	if !scoped {
		return all, nil
	}
	out := []Session{}
	for _, s := range all {
		if inScope(ctx, s, scope) {
			out = append(out, s)
		}
	}
	return out, nil
}

// scoped returns session id when ctx's scope (if any) holds it; a session
// of another repository answers like an unknown ID.
func (m *manager) scoped(ctx context.Context, st *fileStore, id ID) (Session, error) {
	s, err := st.Get(id)
	if err != nil {
		return Session{}, err
	}
	if scope, ok := ScopeFrom(ctx); ok && !inScope(ctx, s, scope) {
		return Session{}, ErrNoSession
	}
	return s, nil
}

func (m *manager) Get(ctx context.Context, id ID) (Session, []AskRecord, error) {
	st, err := m.store()
	if err != nil {
		return Session{}, nil, err
	}
	s, err := m.scoped(ctx, st, id)
	if err != nil {
		return Session{}, nil, err
	}
	asks, err := st.Asks(id)
	return s, asks, err
}

func (m *manager) Items(ctx context.Context, id ID, since int64, max int) ([]Item, int64, error) {
	st, err := m.store()
	if err != nil {
		return nil, since, err
	}
	if _, err := m.scoped(ctx, st, id); err != nil {
		return nil, since, err
	}
	items, err := st.Items(id, since, max)
	next := since
	if n := len(items); n > 0 {
		next = items[n-1].Seq
	}
	return items, next, err
}

// ---- scope: a caller sees only its own repository's sessions ----

type scopeKey struct{}

// WithScope limits what List, Get, Items, Answer and Cancel see to the
// sessions of the repository whose common git directory is common (every
// worktree of one repository shares it). A session of another repository
// answers like an unknown ID. Without a scope (the projection, the host)
// every session is seen.
func WithScope(ctx context.Context, common string) context.Context {
	return context.WithValue(ctx, scopeKey{}, resolvedPath(common))
}

// ScopeFrom returns ctx's scope.
func ScopeFrom(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(scopeKey{}).(string)
	return s, ok
}

// inScope reports whether s belongs to the repository at common. A record
// written before Common existed is matched through its worktree; one whose
// worktree is gone is in no scope.
func inScope(ctx context.Context, s Session, common string) bool {
	c := s.Common
	if c == "" {
		_, _, cd, _, err := worktreeOf(ctx, s.Worktree)
		if err != nil {
			return false
		}
		c = cd
	}
	return common != "" && sameDir(c, common)
}

// fingerprintMAC is the allow answer's HMAC of the fingerprint.
func fingerprintMAC(key []byte, fingerprint string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(fingerprint))
	return hex.EncodeToString(mac.Sum(nil))
}

// Answer is the one place the answer rules live ("Phase 7 who may start a
// session and who may answer").
func (m *manager) Answer(ctx context.Context, r AnswerRequest) (AnswerResult, error) {
	req := requester(ctx)
	if selfOrSession(req) {
		return AnswerResult{}, refuse(AnswerSelf, "A session never answers its own requests.", "")
	}
	if req.Kind != RequesterPerson && req.Kind != RequesterAgent {
		return AnswerResult{}, refuse(AnswerSelf, "Only a person or an agent may answer a session's request.", "")
	}
	if r.Decision != Allow && r.Decision != Deny {
		return AnswerResult{}, refuse(AnswerNotFound, "The answer must allow or reject.", "")
	}
	if r.Decision == Allow && req.Kind != RequesterPerson {
		return AnswerResult{}, refuse(AnswerNeedsPerson, "Only a person may allow a session's request.", "Ask the person to answer it in the console's AI panel or with agentflow ai respond --allow in their terminal.")
	}
	st, err := m.store()
	if err != nil {
		return AnswerResult{}, err
	}
	sess, err := m.scoped(ctx, st, r.Session)
	if err != nil {
		return AnswerResult{}, refuse(AnswerNotFound, "There is no such session.", "")
	}
	if sess.State == StateEnded {
		return AnswerResult{}, refuse(AnswerEnded, "The session has ended.", "")
	}
	ask, err := st.GetAsk(r.Session, r.Ask)
	if err != nil {
		return AnswerResult{}, refuse(AnswerNotFound, "The session has no such waiting request.", "")
	}
	if _, answered, _ := st.ReadAnswer(r.Session, r.Ask); answered {
		return AnswerResult{}, refuse(AnswerAnswered, "This request was already answered.", "")
	}
	if !m.o.Now().Before(ask.ExpiresAt) {
		return AnswerResult{}, refuse(AnswerExpired, "This request has expired.", "")
	}
	if r.Decision == Allow && r.Fingerprint == "" {
		return AnswerResult{}, refuse(AnswerFingerprint, "An allow must name the request as it was shown.", "Answer from the console's AI panel or with agentflow ai respond --allow.")
	}
	if r.Fingerprint != "" && !hmac.Equal([]byte(r.Fingerprint), []byte(ask.Fingerprint)) {
		return AnswerResult{}, refuse(AnswerFingerprint, "This request changed since it was shown.", "")
	}
	if r.Decision == Allow && !ask.CanAllow {
		return AnswerResult{}, refuse(AnswerCannotAllow, "The harness offered no way to allow this request once.", "")
	}
	rec := AnswerRecord{Ask: ask.ID, Decision: r.Decision, By: req, At: m.o.Now().UTC(), Fingerprint: ask.Fingerprint}
	if r.Decision == Allow {
		key, err := st.AnswerKey(r.Session)
		if err != nil {
			return AnswerResult{}, err
		}
		rec.MAC = fingerprintMAC(key, ask.Fingerprint)
	}
	if err := st.WriteAnswer(r.Session, rec); err != nil {
		return AnswerResult{}, err
	}
	return AnswerResult{Session: r.Session, Ask: ask.ID, Decision: r.Decision, State: sess.State}, nil
}

func (m *manager) Cancel(ctx context.Context, id ID) (Session, error) {
	req := requester(ctx)
	if selfOrSession(req) {
		return Session{}, refuse(AnswerSelf, "A session cannot cancel itself.", "")
	}
	if req.Kind != RequesterPerson && req.Kind != RequesterAgent {
		return Session{}, refuse(AnswerSelf, "Only a person or an agent may cancel a session.", "")
	}
	st, err := m.store()
	if err != nil {
		return Session{}, err
	}
	sess, err := m.scoped(ctx, st, id)
	if err != nil {
		return Session{}, refuse(AnswerNotFound, "There is no such session.", "")
	}
	if sess.State == StateEnded {
		return sess, nil
	}
	if err := st.RequestCancel(id, req); err != nil {
		return Session{}, err
	}
	return st.Get(id)
}

func (m *manager) Prune(context.Context) error {
	st, err := m.store()
	if err != nil {
		return err
	}
	user, err := config.LoadUserAt(m.o.Home)
	if err != nil {
		user = &config.User{}
	}
	return st.Prune(user.AI.Sessions.EffectiveKeepDays(), user.AI.Sessions.KeepTranscriptsOn())
}

// auditor writes the session audit lines.
func newAuditGate(home string, user *config.User, repo *config.Repo, common string) (*policy.Gate, error) {
	g, err := policy.NewGate(home, config.Effective(user, repo), "ai-host")
	if err != nil {
		return nil, err
	}
	g.Configure(common, user, repo)
	return g, nil
}
