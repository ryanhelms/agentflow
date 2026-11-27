package aisession

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/boundary"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// maxUpdateBytes bounds what an agent may send in session/update text
// before the session ends limit_output.
const maxUpdateBytes = 64 << 20

// askPoll is how often a held ask looks for its answer file.
const askPoll = 100 * time.Millisecond

// host runs one session (the Process Manager's worker). It is the
// acp.ClientHandler the harness talks to.
type host struct {
	m  *manager
	st *fileStore
	o  HostOptions
	id ID

	user   *config.User
	repo   *config.Repo
	w      worktree
	files  *files
	chain  []PermissionHandler
	gate   *policy.Gate
	env    sessionEnv
	limits Limits
	ev     *events
	terms  *terminals

	proc         jsonrpc.Process
	agentSession string

	mu          sync.Mutex
	sess        Session
	waiting     map[string]chan struct{}
	askTimes    []time.Time
	tools       map[string]*toolState
	activity    time.Time
	updateBytes int64
	cancelling  bool
	cancelCode  string
	cancelCh    chan struct{}
}

// toolState is what the host follows of one tool call.
type toolState struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Kind      acp.ToolKind   `json:"kind"`
	Status    string         `json:"status"`
	Locations []string       `json:"locations,omitempty"`
	Diffs     []toolDiff     `json:"diffs,omitempty"`
	Terminal  string         `json:"terminal,omitempty"`
	Asked     bool           `json:"asked"`
	Unasked   bool           `json:"unasked"`
	paths     []Path         // resolved locations
	terminals map[string]int // terminal IDs named in its content
}

type toolDiff struct {
	Path    string `json:"path"`
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text"`
	Cut     bool   `json:"cut,omitempty"`
}

func newHost(m *manager, st *fileStore, id ID, o HostOptions) (*host, error) {
	if o.Mode == "" {
		o.Mode = HostDetached
	}
	sess, err := st.Get(id)
	if err != nil {
		return nil, err
	}
	if sess.State != StateStarting {
		return nil, refuse(RefuseNested, "The session is not waiting for a host.", "")
	}
	// Claim it: the first host wins.
	f, err := os.OpenFile(st.path(id, claimFile), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, refuse(RefuseNested, "Another host has claimed the session.", "")
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	_ = f.Close()
	h := &host{m: m, st: st, o: o, id: id, sess: sess, waiting: map[string]chan struct{}{}, tools: map[string]*toolState{},
		terms: newTerminals(), cancelCh: make(chan struct{}), activity: m.o.Now()}
	h.ev = newEvents(m.o, sess)
	return h, nil
}

func (h *host) now() time.Time { return h.m.o.Now() }

// transition moves the session to state with code, under the store lock,
// and publishes ai.session.changed.
func (h *host) transition(ctx context.Context, to State, code string, fn func(*Session)) Session {
	h.mu.Lock()
	counters, usage := h.sess.Counters, h.sess.Usage
	h.mu.Unlock()
	var prev State
	s, err := h.st.Update(h.id, func(s *Session) error {
		prev = s.State
		if s.State == StateEnded {
			return errors.New("ended")
		}
		if to != "" {
			s.State = to
		}
		if code != "" {
			s.Code = code
		}
		s.Counters, s.Usage = counters, usage
		if fn != nil {
			fn(s)
		}
		return nil
	})
	if err != nil {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.sess
	}
	h.mu.Lock()
	// Counters counted while the store was updated are kept.
	c, u := h.sess.Counters, h.sess.Usage
	h.sess = s
	h.sess.Counters, h.sess.Usage = c, u
	h.mu.Unlock()
	if prev != s.State {
		h.ev.changed(ctx, s, prev)
	}
	return s
}

func (h *host) state() State {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sess.State
}

// record stores transcript items and hands them to the foreground and the
// output aggregator.
func (h *host) record(ctx context.Context, items ...Item) {
	if len(items) == 0 {
		return
	}
	stored, err := h.st.Append(h.id, items...)
	if err != nil {
		return
	}
	var n int64
	for _, it := range stored {
		n += int64(len(it.Text) + len(it.Data))
	}
	h.mu.Lock()
	h.sess.Counters.TranscriptBytes += n
	h.mu.Unlock()
	if h.o.OnItems != nil {
		h.o.OnItems(stored)
	}
	h.ev.output(ctx, stored)
}

func dataOf(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}

// Run hosts the session until it ends.
func (h *host) Run(ctx context.Context) (Session, error) {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	start := h.now()
	h.transition(ctx, "", "", func(s *Session) {
		s.Host = HostInfo{PID: os.Getpid(), Mode: h.o.Mode, StartedAt: start.UTC(), Heartbeat: start.UTC()}
	})
	_ = h.st.beat(h.id)
	go h.heartbeat(ctx)

	code, err := h.setup(ctx)
	if err != nil {
		return h.end(ctx, code), nil
	}
	return h.end(ctx, h.prompt(ctx, start)), nil
}

func (h *host) heartbeat(ctx context.Context) {
	t := time.NewTicker(HeartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_ = h.st.beat(h.id)
		}
	}
}

// setup launches the harness and runs initialize, session/new and
// set_mode. It returns the end code on failure.
func (h *host) setup(ctx context.Context) (string, error) {
	fail := func(code string) (string, error) { return code, errors.New(code) }
	home := h.m.o.Home
	user, err := config.LoadUserAt(home)
	if err != nil {
		return fail(CodeLaunch)
	}
	h.user = user
	sess := h.sess
	repo, _, err := config.LoadRepoCore(sess.Worktree)
	if err != nil || repo == nil {
		repo = &config.Repo{}
	}
	h.repo = repo
	root, gitDir, common, branch, err := worktreeOf(ctx, sess.Worktree)
	if err != nil || gitDir == common || !sameDir(root, sess.Worktree) {
		return fail(CodeLaunch)
	}
	if h.w, err = newWorktree(root); err != nil {
		return fail(CodeLaunch)
	}
	if h.files, err = openFiles(h.w); err != nil {
		return fail(CodeLaunch)
	}
	s := user.AI.Sessions
	h.limits = Limits{MaxRunning: s.EffectiveMaxRunning(), MaxDuration: s.EffectiveMaxDuration(), IdleTimeout: s.EffectiveIdleTimeout(), AskTimeout: s.EffectiveAskTimeout()}

	found, hh, err := harness.Locate(ctx, home, h.m.o.Version, func() []string { return []string{root} }, sess.Harness)
	if err != nil || found.Identity != sess.Identity {
		return fail(CodeLaunch)
	}
	if hh.State == "" {
		return fail(CodeLaunch)
	}
	if harness.GuardPath(found.Path) != nil || harness.GuardPath(found.Target) != nil {
		return fail(CodeLaunch)
	}
	dir := h.st.Dir(h.id)
	accountHome, _ := config.AccountHome()
	env, ref := buildEnv(ctx, envInput{
		Parent: os.Environ(), AccountHome: accountHome, SessionDir: dir, HomeMode: sess.Home,
		HarnessDir: found.PathDir, Definition: found.Definition, WorkDirs: []string{root}, Session: h.id, Windows: isWindows(),
	})
	if ref != nil {
		return fail(CodeLaunch)
	}
	h.env = env
	if err := mkdirPrivate(env.Tmp); err != nil {
		return fail(CodeLaunch)
	}
	if sess.Home == config.AISessionHomeIsolated {
		if err := mkdirPrivate(env.Home); err != nil {
			return fail(CodeLaunch)
		}
	}
	gate, err := newAuditGate(home, user, repo, common)
	if err != nil {
		return fail(CodeLaunch)
	}
	h.gate = gate
	repoHome, _ := boundary.ResolveDir(ctx, root)
	expand := map[string][]string{}
	if v := os.Getenv("HOME"); v != "" {
		expand["HOME"] = append(expand["HOME"], v)
	}
	if accountHome != "" {
		expand["HOME"] = append(expand["HOME"], accountHome)
	}
	if v := os.Getenv("AGENTFLOW_HOME"); v != "" {
		expand["AGENTFLOW_HOME"] = append(expand["AGENTFLOW_HOME"], v)
	}
	expand["AGENTFLOW_HOME"] = append(expand["AGENTFLOW_HOME"], home)
	protect := []string{home, filepath.Join(home, storeDir)}
	for _, hd := range expand["HOME"] {
		protect = append(protect, filepath.Join(hd, ".agentflow-ui"))
	}
	h.chain = DefaultChain(ChainConfig{
		Session: h.id, Worktree: root, Ceiling: sess.Ceiling, User: user, Repo: repo, Gate: gate,
		Protected: repo.Policy.ProtectedBranches, Branch: branch, CoordinatorMerge: repo.Policy.CoordinatorMerge,
		MergeBase: h.m.o.MergeBase, RepoHome: repoHome, Resolve: boundary.GitResolver(ctx),
		Protect: protect, Expand: expand, PluginDeny: h.m.o.PluginDeny, InTest: testing.Testing(), Guard: harness.GuardPath,
	})

	logf, _ := os.OpenFile(filepath.Join(dir, hostLogFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	var stderr io.Writer
	if logf != nil {
		defer func() {
			if h.proc == nil {
				_ = logf.Close()
			}
		}()
		stderr = logf
	}
	proc, err := jsonrpc.Launch(ctx, jsonrpc.LaunchSpec{
		Path: found.Path, Root: harness.LaunchRoot(found.PathDir, found.Target), Dir: root, AllowDir: root, DataDir: dir,
		Args: append([]string{}, found.Definition.Args...), Env: env.Harness, AllowEnv: env.AllowEnv,
		Stderr: stderr, StderrLimit: 1 << 20, Limits: acp.SessionLimits, Handler: acp.NewClientHandler(h),
	})
	if err != nil {
		return fail(CodeLaunch)
	}
	h.proc = proc
	if logf != nil {
		go func() { <-proc.Exited(); _ = logf.Close() }()
	}
	writeHarnessProc(h.st.path(h.id, harnessPIDFile), proc.PID())
	h.transition(ctx, StateInitializing, "", func(s *Session) { s.Host.HarnessPID = proc.PID() })

	callCtx, cancel := context.WithTimeout(ctx, SetupTimeout)
	init, err := acp.Initialize(callCtx, proc.Conn(), acp.SessionInitialize(h.m.o.Version))
	cancel()
	if err != nil {
		return fail(setupCode(err, callCtx))
	}
	if init.ProtocolVersion != 1 {
		return fail(CodeProtocolVersion)
	}
	h.transition(ctx, StateCreating, "", nil)

	servers := []acp.MCPServer{}
	if mc := h.m.mcpServer(h.id, root, s.AgentflowMCPOn()); mc.ok {
		servers = append(servers, *h.m.o.MCPServer(h.id))
	}
	callCtx, cancel = context.WithTimeout(ctx, SetupTimeout)
	ns, err := acp.NewSession(callCtx, proc.Conn(), acp.NewSessionRequest{Cwd: root, MCPServers: servers})
	cancel()
	if err != nil {
		var rerr *jsonrpc.Error
		if errors.As(err, &rerr) && rerr.Code == acp.CodeAuthRequired {
			_ = harness.NewFileStore(home).MarkAuthRequired(found.ID, found.Identity, h.now())
			return fail(CodeAuthRequired)
		}
		return fail(setupCode(err, callCtx))
	}
	h.agentSession = ns.SessionID
	if h.agentSession == "" || len(h.agentSession) > 1024 {
		return fail(CodeProtocol)
	}
	mode := ""
	if want := s.Modes[sess.Harness]; want != "" && ns.Modes != nil {
		for _, md := range ns.Modes.AvailableModes {
			if md.ID == want {
				callCtx, cancel = context.WithTimeout(ctx, SetupTimeout)
				err := acp.SetMode(callCtx, proc.Conn(), acp.SetSessionModeRequest{SessionID: ns.SessionID, ModeID: want})
				cancel()
				if err != nil {
					return fail(setupCode(err, callCtx))
				}
				mode = want
			}
		}
	}
	h.transition(ctx, StateRunning, "", func(s *Session) {
		s.AgentSession = SanitizeLine(ns.SessionID, 128)
		s.Mode = SanitizeLine(mode, 128)
		s.MCP = len(servers) > 0
	})
	h.touch()
	return "", nil
}

// setupCode maps a setup call's error to an end code.
func setupCode(err error, ctx context.Context) string {
	var rerr *jsonrpc.Error
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return CodeTimeout
	case errors.As(err, &rerr), errors.Is(err, jsonrpc.ErrProtocol), errors.Is(err, acp.ErrProtocol), errors.Is(err, jsonrpc.ErrFrameTooLarge):
		return CodeProtocol
	case errors.Is(err, jsonrpc.ErrClosed):
		return CodeExited
	}
	return CodeProtocol
}

// prompt runs the one prompt turn and returns the end code.
func (h *host) prompt(ctx context.Context, start time.Time) string {
	items, _ := h.st.Items(h.id, 0, 1)
	text := ""
	if len(items) > 0 && items[0].Kind == ItemPrompt {
		text = items[0].Text
	}
	deadline := start.Add(h.limits.MaxDuration + CancelWait + 5*time.Second)
	pctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	done := make(chan struct{})
	go h.watch(ctx, start, done)
	defer close(done)
	res, err := acp.Prompt(pctx, h.proc.Conn(), acp.PromptRequest{SessionID: h.agentSession, Prompt: []acp.ContentBlock{{Type: "text", Text: text}}})
	h.mu.Lock()
	cancelling, cancelCode := h.cancelling, h.cancelCode
	h.mu.Unlock()
	if cancelling {
		return cancelCode
	}
	if err != nil {
		if errors.Is(pctx.Err(), context.DeadlineExceeded) {
			return CodeLimitDuration
		}
		var rerr *jsonrpc.Error
		if errors.As(err, &rerr) || errors.Is(err, jsonrpc.ErrProtocol) || errors.Is(err, jsonrpc.ErrFrameTooLarge) {
			return CodeProtocol
		}
		return CodeExited
	}
	switch res.StopReason {
	case acp.StopEndTurn, acp.StopMaxTokens, acp.StopMaxTurnRequests, acp.StopRefusal, acp.StopCancelled:
		return string(res.StopReason)
	}
	return CodeProtocol
}

// watch enforces the limits and picks up cancel requests while the turn
// runs.
func (h *host) watch(ctx context.Context, start time.Time, done <-chan struct{}) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-h.proc.Exited():
			return
		case <-t.C:
		}
		now := h.now()
		h.mu.Lock()
		idle := now.Sub(h.activity) > h.limits.IdleTimeout && len(h.waiting) == 0
		output := h.updateBytes > maxUpdateBytes
		h.mu.Unlock()
		switch {
		case h.st.CancelRequested(h.id):
			h.cancel(ctx, CodeCancelled)
		case now.Sub(start) > h.limits.MaxDuration:
			h.cancel(ctx, CodeLimitDuration)
		case idle:
			h.cancel(ctx, CodeLimitIdle)
		case output:
			h.cancel(ctx, CodeLimitOutput)
		}
	}
}

func (h *host) touch() {
	h.mu.Lock()
	h.activity = h.now()
	h.mu.Unlock()
}

// cancel starts cancelling: session/cancel, every waiting ask answered
// cancelled at once, held requests answered -32800, then up to CancelWait
// for the turn to end before the process is stopped.
func (h *host) cancel(ctx context.Context, code string) {
	h.mu.Lock()
	if h.cancelling {
		h.mu.Unlock()
		return
	}
	h.cancelling, h.cancelCode = true, code
	close(h.cancelCh)
	h.mu.Unlock()
	h.record(ctx, Item{Kind: ItemLimit, Text: code})
	h.transition(ctx, StateCancelling, code, nil)
	if h.proc != nil {
		cctx, cancel := context.WithTimeout(ctx, time.Second)
		_ = acp.Cancel(cctx, h.proc.Conn(), h.agentSession)
		cancel()
		proc := h.proc
		go func() {
			select {
			case <-proc.Exited():
			case <-time.After(CancelWait):
				_ = proc.Kill()
			}
		}()
	}
}

func (h *host) isCancelling() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cancelling
}

// end stops everything, records what changed and ends the session.
func (h *host) end(ctx context.Context, code string) Session {
	if h.proc != nil {
		done := make(chan struct{})
		go func() { _ = h.proc.Stop(); close(done) }()
		select {
		case <-done:
		case <-time.After(StopGrace + 3*time.Second):
			_ = h.proc.Kill()
		}
	}
	h.terms.killAll()
	if h.files != nil {
		_ = h.files.Close()
	}
	var changed []string
	if h.w.real != "" {
		changed = changedFiles(ctx, h.w.real)
	}
	_ = os.RemoveAll(filepath.Join(h.st.Dir(h.id), "tmp"))
	// No ask outlives its session: their previews go with them.
	_ = os.RemoveAll(h.st.path(h.id, asksDir))
	h.ev.flush(ctx)
	s := h.transition(ctx, StateEnded, code, func(s *Session) {
		s.ChangedFiles = changed
		t := h.now().UTC()
		s.EndedAt = &t
	})
	h.ev.ended(ctx, s)
	keep := h.user == nil || h.user.AI.Sessions.KeepTranscriptsOn()
	if !keep {
		h.st.dropTranscript(h.id)
	}
	if h.user != nil {
		_ = h.st.Prune(h.user.AI.Sessions.EffectiveKeepDays(), keep)
	}
	return s
}

// changedFiles lists the worktree's changed paths, worktree-relative.
func changedFiles(ctx context.Context, root string) []string {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	out, err := (&gitx.Repo{Dir: root}).Git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil
	}
	var files []string
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		p := parts[i]
		if len(p) < 4 {
			continue
		}
		status, name := p[:2], p[3:]
		if status[0] == 'R' || status[0] == 'C' {
			i++ // the source path follows
		}
		files = append(files, SanitizeLine(name, 512))
		if len(files) >= MaxChangedFiles {
			break
		}
	}
	return files
}

// ---- decisions ----

// decide runs the chain and, for an ask, holds the request for a person.
func (h *host) decide(ctx context.Context, r *Request) Verdict {
	h.touch()
	v := Run(ctx, h.chain, r)
	if v.Decision == Ask {
		v = h.ask(ctx, r)
	}
	h.mu.Lock()
	if v.Decision == Allow {
		h.sess.Counters.Allowed++
	} else {
		h.sess.Counters.Denied++
	}
	h.mu.Unlock()
	h.audit(r, v, "")
	h.touch()
	return v
}

func (h *host) audit(r *Request, v Verdict, by string) {
	if h.gate == nil {
		return
	}
	class, action := actionOf(r)
	subject := ""
	switch {
	case r.Command != nil:
		subject = programBase(r.Command.Resolved)
	case len(r.Paths) > 0:
		subject = r.Paths[0].Rel
	}
	if by == "" {
		by = "policy"
		if v.Handler == HandlerPerson {
			by = RequesterPerson
		}
	}
	h.gate.AuditSession(policy.SessionAudit{
		Class: class, Action: action, Session: string(h.id), Kind: string(r.Kind), ToolKind: string(r.ToolKind),
		Subject: subject, Decision: string(v.Decision), Handler: v.Handler, Code: v.Code, By: by,
	})
}

// canAllow: a permission request can be allowed only with an allow_once
// option.
func canAllow(r *Request) bool {
	if r.Kind != KindPermission {
		return true
	}
	return slices.ContainsFunc(r.Options, func(o acp.PermissionOption) bool { return o.Kind == acp.AllowOnce })
}

// fingerprintOf is the SHA-256 of the canonical ask (Fingerprint empty).
func fingerprintOf(a AskRecord) string {
	a.Fingerprint = ""
	b, _ := json.Marshal(a)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ask holds r for a person: the ask file, ai.session.asked, then the
// answer, the timeout (deny) or the cancel.
func (h *host) ask(ctx context.Context, r *Request) Verdict {
	now := h.now()
	h.mu.Lock()
	recent := h.askTimes[:0]
	for _, t := range h.askTimes {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	h.askTimes = recent
	switch {
	case h.cancelling:
		h.mu.Unlock()
		return deny(HandlerCancel, VerdictCancelled)
	case len(h.waiting) >= MaxAsksWaiting:
		h.mu.Unlock()
		return deny(HandlerLimit, VerdictTooManyAsks)
	case len(h.askTimes) >= MaxAsksPerMinute:
		h.mu.Unlock()
		return deny(HandlerLimit, VerdictRate)
	}
	h.askTimes = append(h.askTimes, now)
	h.sess.Counters.Asks++
	rec := AskRecord{
		ID: newAskID(), Session: h.id, Kind: r.Kind, ToolCallID: r.ToolCallID, ToolKind: r.ToolKind, Title: r.Title,
		Options: r.Options, Size: r.Size, Reason: r.Reason, CanAllow: canAllow(r),
		CreatedAt: now.UTC(), ExpiresAt: now.Add(h.limits.AskTimeout).UTC(),
	}
	for _, p := range r.Paths {
		rec.Paths = append(rec.Paths, display(p))
	}
	if c := r.Command; c != nil {
		rec.Line, rec.Cwd, rec.EnvNames = c.Line, c.Cwd.Rel, c.EnvNames
		if rec.Cwd == "" {
			rec.Cwd = "."
		}
	}
	if r.Kind == KindWrite {
		pv, ok := previewFrom(ctx)
		if !ok {
			pv.diff.Cut = true
		}
		d := pv.diff
		rec.Diff, rec.NewSHA256 = &d, pv.newSHA256
		if d.Cut {
			rec.CanAllow, rec.Reason = false, ReasonPreviewTooLarge
		}
	}
	rec.Fingerprint = fingerprintOf(rec)
	signal := make(chan struct{})
	h.waiting[rec.ID] = signal
	first := len(h.waiting) == 1
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.waiting, rec.ID)
		none := len(h.waiting) == 0
		h.mu.Unlock()
		_ = h.st.RemoveAsk(h.id, rec.ID)
		if none && h.state() == StateWaiting {
			h.transition(ctx, StateRunning, "", nil)
		}
	}()
	if err := h.st.PutAsk(rec); err != nil {
		return deny(HandlerLimit, VerdictIO)
	}
	if first && h.state() == StateRunning {
		h.transition(ctx, StateWaiting, "", nil)
	}
	h.ev.asked(ctx, rec)
	// The transcript never holds the write preview.
	shown := rec
	shown.Diff = nil
	h.record(ctx, Item{Kind: ItemAsk, ToolCall: rec.ToolCallID, Data: dataOf(shown)})
	if h.o.OnAsk != nil {
		h.o.OnAsk(rec)
	}

	v, by := h.awaitAnswer(ctx, rec)
	h.ev.answered(ctx, rec.ID, v, by)
	h.record(ctx, Item{Kind: ItemAnswer, ToolCall: rec.ToolCallID, Data: dataOf(map[string]string{
		"ask": rec.ID, "decision": string(v.Decision), "by": by, "handler": v.Handler, "code": v.Code,
	})})
	h.audit(r, v, by)
	return v
}

// awaitAnswer waits for the answer file. Only a person's allow with a
// valid HMAC over this ask's fingerprint allows; anything else rejects.
func (h *host) awaitAnswer(ctx context.Context, rec AskRecord) (Verdict, string) {
	t := time.NewTicker(askPoll)
	defer t.Stop()
	for {
		if a, ok, _ := h.st.ReadAnswer(h.id, rec.ID); ok {
			by := a.By.Kind
			if by != RequesterPerson && by != RequesterAgent {
				by = "unknown"
			}
			if a.Decision == Allow && h.validAllow(rec, a) {
				return Verdict{Decision: Allow, Handler: HandlerPerson, Code: VerdictAllowed}, by
			}
			return Verdict{Decision: Deny, Handler: HandlerPerson, Code: VerdictRejected}, by
		}
		if !h.now().Before(rec.ExpiresAt) {
			return Verdict{Decision: Deny, Handler: HandlerTimeout, Code: VerdictExpired}, ""
		}
		select {
		case <-ctx.Done():
			return Verdict{Decision: Deny, Handler: HandlerCancel, Code: VerdictCancelled}, ""
		case <-h.cancelCh:
			return Verdict{Decision: Deny, Handler: HandlerCancel, Code: VerdictCancelled}, ""
		case <-t.C:
		}
	}
}

func (h *host) validAllow(rec AskRecord, a AnswerRecord) bool {
	if a.By.Kind != RequesterPerson || strings.HasPrefix(a.By.Client, SessionClientPrefix) || a.By.Session != "" {
		return false
	}
	if !rec.CanAllow || a.Fingerprint != rec.Fingerprint || a.At.After(rec.ExpiresAt) {
		return false
	}
	key, err := h.st.AnswerKey(h.id)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(a.MAC), []byte(fingerprintMAC(key, rec.Fingerprint)))
}
