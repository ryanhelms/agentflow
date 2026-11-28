package aisession

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"unicode/utf8"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// The acp.ClientHandler side of the host (the Proxy): every agent request
// becomes a Request, goes through the chain, and only then does agentflow
// act. Denials are -32001 with a fixed message and {"code": "<verdict>"};
// a missing file is -32002; a request held while the session cancels is
// -32800. No message echoes a path or the agent's own text.

var verdictMessages = map[string]string{
	VerdictOutsideWorktree: "the path is outside the session's worktree",
	VerdictGitDir:          "git directories are not accessible",
	VerdictRelative:        "the path must be absolute",
	VerdictTooLarge:        "the file is too large",
	VerdictNotText:         "the file is not UTF-8 text",
	VerdictNotRegular:      "not a regular file",
}

func denied(code string) error {
	msg := verdictMessages[code]
	if msg == "" {
		msg = "denied by agentflow"
	}
	data, _ := json.Marshal(jsonrpc.ErrorData{Code: code})
	return &jsonrpc.Error{Code: acp.CodeDenied, Message: msg, Data: data}
}

func notFound() error {
	data, _ := json.Marshal(jsonrpc.ErrorData{Code: VerdictNotFound})
	return &jsonrpc.Error{Code: acp.CodeResourceNotFound, Message: "resource not found", Data: data}
}

func cancelledErr() error {
	return &jsonrpc.Error{Code: acp.CodeRequestCancelled, Message: "request cancelled"}
}

func (h *host) wrongSession(id string) bool { return h.agentSession == "" || id != h.agentSession }

// ---- session/update ----

func (h *host) SessionUpdate(ctx context.Context, n acp.SessionNotification) {
	if h.wrongSession(n.SessionID) {
		return
	}
	u := n.Update
	h.mu.Lock()
	h.activity = h.now()
	h.sess.Counters.Updates++
	h.mu.Unlock()
	var items []Item
	add := func(it Item) { items = append(items, it) }
	switch u.Kind {
	case acp.UpdateAgentMessageChunk, acp.UpdateAgentThoughtChunk:
		kind := ItemMessage
		if u.Kind == acp.UpdateAgentThoughtChunk {
			kind = ItemThought
		}
		// Each item is one chunk, kept as sent (sanitized): the reader joins
		// consecutive items of the same kind with no separator.
		if u.Content != nil {
			text := u.Content.Text
			if u.Content.Type != "text" {
				text = "[" + SanitizeLine(u.Content.Type, 32) + "]"
			}
			h.countBytes(len(text))
			add(Item{Kind: kind, Text: Sanitize(text, MaxItemText)})
			if h.draft && kind == ItemMessage && u.Content.Type == "text" {
				h.draftAnswer(ctx, u.Content.Text)
			}
		}
	case acp.UpdateUserMessageChunk:
	case acp.UpdateToolCall:
		if u.ToolCall != nil {
			ts := h.toolCall(ctx, *u.ToolCall)
			add(Item{Kind: ItemTool, ToolCall: ts.ID, Data: dataOf(ts)})
		}
	case acp.UpdateToolCallUpdate:
		if u.ToolCallUpdate != nil {
			ts := h.toolCallUpdate(ctx, *u.ToolCallUpdate)
			add(Item{Kind: ItemTool, ToolCall: ts.ID, Data: dataOf(ts)})
		}
	case acp.UpdatePlan:
		if u.Plan != nil {
			var entries []map[string]string
			for i, e := range u.Plan.Entries {
				if i >= 64 {
					break
				}
				entries = append(entries, map[string]string{
					"content": SanitizeLine(e.Content, 1024), "priority": SanitizeLine(e.Priority, 16), "status": SanitizeLine(e.Status, 16),
				})
			}
			add(Item{Kind: ItemPlan, Data: dataOf(map[string]any{"entries": entries})})
		}
	case acp.UpdateUsage:
		if u.Usage != nil {
			usage := &Usage{Used: u.Usage.Used, Size: u.Usage.Size}
			if c := u.Usage.Cost; c != nil {
				usage.CostAmount, usage.CostCurrency = c.Amount, SanitizeLine(c.Currency, 8)
			}
			h.mu.Lock()
			h.sess.Usage = usage
			h.mu.Unlock()
			add(Item{Kind: ItemUsage, Data: dataOf(map[string]any{
				"used": usage.Used, "size": usage.Size, "cost_amount": usage.CostAmount, "cost_currency": usage.CostCurrency, "cost_reported": u.Usage.Cost != nil,
			})})
		}
	case acp.UpdateCurrentMode:
		add(Item{Kind: ItemMode, Text: SanitizeLine(u.CurrentModeID, 128)})
	case acp.UpdateSessionInfo, acp.UpdateAvailableCommands, acp.UpdateConfigOption:
		add(Item{Kind: ItemInfo, Text: string(u.Kind)})
	default:
		// Recorded as unknown, never acted on; its content is not kept.
		h.countBytes(len(u.Raw))
		add(Item{Kind: ItemUnknown, Text: SanitizeLine(string(u.Kind), 64)})
	}
	h.record(ctx, items...)
}

func (h *host) countBytes(n int) {
	h.mu.Lock()
	h.updateBytes += int64(n)
	h.mu.Unlock()
}

// ---- tool calls, and the ones the harness did not ask about ----

// maxToolCalls bounds the tool calls the host follows in memory, and
// maxDiffBytes the diff text it holds for them. Past the first the session
// ends limit_output (dropping tool calls would blind the unasked check);
// past the second, diffs are kept as cut, without text. Variables only for
// tests.
var (
	maxToolCalls       = 4096
	maxDiffBytes int64 = 32 << 20
)

// tool returns the state of tool call id, creating it; ok is false past
// maxToolCalls, when the session is cancelled limit_output instead.
func (h *host) tool(ctx context.Context, id string, init func() *toolState) (*toolState, bool) {
	h.mu.Lock()
	ts, ok := h.tools[id]
	if !ok {
		if len(h.tools) >= maxToolCalls {
			h.mu.Unlock()
			go h.cancel(context.WithoutCancel(ctx), CodeLimitOutput)
			return nil, false
		}
		ts = init()
		h.tools[id] = ts
		h.sess.Counters.ToolCalls++
	}
	return ts, true
}

func (h *host) toolCall(ctx context.Context, tc acp.ToolCall) *toolState {
	ts, ok := h.tool(ctx, tc.ToolCallID, func() *toolState {
		return &toolState{ID: SanitizeLine(tc.ToolCallID, 128), terminals: map[string]int{}}
	})
	if !ok {
		return &toolState{ID: SanitizeLine(tc.ToolCallID, 128)}
	}
	ts.Title = SanitizeLine(tc.Title, 512)
	ts.Kind = tc.Kind
	if ts.Kind == "" {
		ts.Kind = acp.ToolOther
	}
	ts.Status = string(tc.Status)
	if ts.Status == "" {
		ts.Status = string(acp.ToolPending)
	}
	h.mu.Unlock()
	h.toolContent(ts, tc.Content, tc.Locations)
	h.checkTool(ctx, ts)
	return ts.snapshot(&h.mu)
}

// checkTool runs the unasked check, or for a draft the draft's tool rule.
func (h *host) checkTool(ctx context.Context, ts *toolState) {
	if h.draft {
		h.draftTool(ctx, ts)
		return
	}
	h.checkUnasked(ctx, ts)
}

func (h *host) toolCallUpdate(ctx context.Context, u acp.ToolCallUpdate) *toolState {
	ts, ok := h.tool(ctx, u.ToolCallID, func() *toolState {
		return &toolState{ID: SanitizeLine(u.ToolCallID, 128), Kind: acp.ToolOther, Status: string(acp.ToolPending), terminals: map[string]int{}}
	})
	if !ok {
		return &toolState{ID: SanitizeLine(u.ToolCallID, 128)}
	}
	if u.Title != nil {
		ts.Title = SanitizeLine(*u.Title, 512)
	}
	if u.Kind != nil {
		ts.Kind = *u.Kind
	}
	if u.Status != nil {
		ts.Status = string(*u.Status)
	}
	h.mu.Unlock()
	h.toolContent(ts, u.Content, u.Locations)
	h.checkTool(ctx, ts)
	return ts.snapshot(&h.mu)
}

func (ts *toolState) snapshot(mu interface {
	Lock()
	Unlock()
}) *toolState {
	mu.Lock()
	defer mu.Unlock()
	c := *ts
	c.Locations = append([]string{}, ts.Locations...)
	c.Diffs = append([]toolDiff{}, ts.Diffs...)
	return &c
}

func (h *host) toolContent(ts *toolState, content []acp.ToolCallContent, locs []acp.ToolCallLocation) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(locs) > 0 {
		ts.Locations, ts.paths = nil, nil
		for i, l := range locs {
			if i >= 32 {
				break
			}
			p, _ := h.w.resolve(l.Path)
			ts.paths = append(ts.paths, p)
			ts.Locations = append(ts.Locations, SanitizeLine(display(p), 1024))
		}
	}
	for _, c := range content {
		switch c.Type {
		case "diff":
			d := toolDiff{Path: SanitizeLine(c.Path, 1024), NewText: Sanitize(c.NewText, MaxItemText)}
			if p, _ := h.w.resolve(c.Path); p.Rel != "" || p.Abs != "" {
				d.Path = SanitizeLine(display(p), 1024)
			}
			if c.OldText != nil {
				d.OldText = Sanitize(*c.OldText, MaxItemText)
				d.Cut = len(*c.OldText) > MaxItemText
			}
			d.Cut = d.Cut || len(c.NewText) > MaxItemText
			if len(ts.Diffs) < 16 {
				if n := int64(len(d.OldText) + len(d.NewText)); h.diffBytes+n > maxDiffBytes {
					d.OldText, d.NewText, d.Cut = "", "", true
				} else {
					h.diffBytes += n
				}
				ts.Diffs = append(ts.Diffs, d)
			}
			if p, _ := h.w.resolve(c.Path); !containsPath(ts.paths, p) {
				ts.paths = append(ts.paths, p)
			}
		case "terminal":
			ts.Terminal = SanitizeLine(c.TerminalID, 128)
			ts.terminals[c.TerminalID]++
		}
	}
}

func containsPath(ps []Path, p Path) bool {
	for _, q := range ps {
		if q.Abs == p.Abs {
			return true
		}
	}
	return false
}

// checkUnasked records a tool call that acted without asking, and cancels
// the session when it names a place outside the worktree, in .git or a
// control file (or always, with ai.sessions.unasked: cancel).
func (h *host) checkUnasked(ctx context.Context, ts *toolState) {
	h.mu.Lock()
	switch {
	case ts.Unasked, ts.Asked:
		h.mu.Unlock()
		return
	}
	switch acp.ToolCallStatus(ts.Status) {
	case acp.ToolInProgress, acp.ToolCompleted, acp.ToolFailed:
	default:
		h.mu.Unlock()
		return
	}
	switch ts.Kind {
	case acp.ToolEdit, acp.ToolDelete, acp.ToolMove, acp.ToolExecute, acp.ToolFetch:
	default:
		h.mu.Unlock()
		return
	}
	for id := range ts.terminals {
		if h.terms.owns(id) {
			h.mu.Unlock()
			return
		}
	}
	paths := append([]Path{}, ts.paths...)
	kind := ts.Kind
	h.mu.Unlock()

	// Would the chain have allowed the equivalent request? (A dry run: no
	// one is asked.)
	r := &Request{Session: h.id, Kind: KindPermission, ToolKind: kind, Paths: paths}
	if v := Run(ctx, h.chain, r); v.Decision == Allow {
		return
	}
	h.mu.Lock()
	ts.Unasked = true
	h.sess.Counters.Unasked++
	h.mu.Unlock()
	outside := false
	for _, p := range paths {
		_, code := h.w.resolve(p.Abs)
		switch code {
		case VerdictOutsideWorktree, VerdictGitDir, VerdictNestedRepository, VerdictRelative, VerdictName:
			outside = true
		}
		forms, out := h.w.forms(p)
		if out {
			outside = true
		}
		for _, f := range forms {
			if IsControlFile(f) {
				outside = true
			}
		}
	}
	h.record(ctx, Item{Kind: ItemUnasked, ToolCall: ts.ID, Data: dataOf(map[string]any{"kind": kind, "outside": outside})})
	if outside || (h.user != nil && h.user.AI.Sessions.EffectiveUnasked() == config.AIUnaskedCancel) {
		go h.cancel(context.WithoutCancel(ctx), CodeUnasked)
	}
}

func (h *host) markAsked(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if ts, ok := h.tools[id]; ok {
		ts.Asked = true
		return
	}
	h.tools[id] = &toolState{ID: SanitizeLine(id, 128), Kind: acp.ToolOther, Status: string(acp.ToolPending), Asked: true, terminals: map[string]int{}}
}

// ---- session/request_permission ----

func (h *host) RequestPermission(ctx context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	cancelled := acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeCancelled}}
	if h.wrongSession(p.SessionID) {
		h.audit(&Request{Session: h.id, Kind: KindPermission}, deny(HandlerNever, VerdictWrongSession), "")
		return pickOption(p.Options, Deny), nil
	}
	if h.isCancelling() {
		return cancelled, nil
	}
	tc := p.ToolCall
	h.markAsked(tc.ToolCallID)
	kind := acp.ToolOther
	h.mu.Lock()
	if ts, ok := h.tools[tc.ToolCallID]; ok && ts.Kind != "" {
		kind = ts.Kind
	}
	h.mu.Unlock()
	if tc.Kind != nil {
		kind = *tc.Kind
	}
	switch kind {
	case acp.ToolRead, acp.ToolEdit, acp.ToolDelete, acp.ToolMove, acp.ToolSearch, acp.ToolExecute, acp.ToolThink, acp.ToolFetch, acp.ToolSwitchMode, acp.ToolOther:
	default:
		kind = acp.ToolOther
	}
	r := &Request{Session: h.id, Kind: KindPermission, ToolCallID: SanitizeLine(tc.ToolCallID, 128), ToolKind: kind}
	if tc.Title != nil {
		r.Title = SanitizeLine(*tc.Title, 512)
	}
	for i, o := range p.Options {
		if i >= 8 {
			break
		}
		r.Options = append(r.Options, acp.PermissionOption{OptionID: SanitizeLine(o.OptionID, 128), Name: SanitizeLine(o.Name, 128), Kind: o.Kind})
	}
	locs := tc.Locations
	if len(locs) == 0 {
		h.mu.Lock()
		if ts, ok := h.tools[tc.ToolCallID]; ok {
			for _, pth := range ts.paths {
				locs = append(locs, acp.ToolCallLocation{Path: pth.Abs})
			}
		}
		h.mu.Unlock()
	}
	for i, l := range locs {
		if i >= 32 {
			break
		}
		pth, _ := h.w.resolve(l.Path)
		r.Paths = append(r.Paths, pth)
	}
	v := h.decide(ctx, r)
	if ctx.Err() != nil || (v.Handler == HandlerCancel) {
		return cancelled, nil
	}
	return pickOption(p.Options, v.Decision), nil
}

// pickOption chooses by kind, never by name: allow is the first
// allow_once; deny is reject_once, else reject_always, else cancelled.
// allow_always is never chosen.
func pickOption(opts []acp.PermissionOption, d Decision) acp.RequestPermissionResponse {
	find := func(k acp.PermissionOptionKind) (string, bool) {
		for _, o := range opts {
			if o.Kind == k && o.OptionID != "" {
				return o.OptionID, true
			}
		}
		return "", false
	}
	if d == Allow {
		if id, ok := find(acp.AllowOnce); ok {
			return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeSelected, OptionID: id}}
		}
	}
	for _, k := range []acp.PermissionOptionKind{acp.RejectOnce, acp.RejectAlways} {
		if id, ok := find(k); ok {
			return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeSelected, OptionID: id}}
		}
	}
	return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeCancelled}}
}

// ---- fs/* ----

func (h *host) ReadTextFile(ctx context.Context, p acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	if h.wrongSession(p.SessionID) {
		return acp.ReadTextFileResponse{}, denied(VerdictWrongSession)
	}
	if h.isCancelling() {
		return acp.ReadTextFileResponse{}, cancelledErr()
	}
	pth, _ := h.w.resolve(p.Path)
	checked, _ := h.w.forms(pth)
	r := &Request{Session: h.id, Kind: KindRead, Paths: []Path{pth}}
	v := h.decide(ctx, r)
	if v.Handler == HandlerCancel || ctx.Err() != nil {
		return acp.ReadTextFileResponse{}, cancelledErr()
	}
	if v.Decision != Allow {
		h.record(ctx, Item{Kind: ItemFile, Data: dataOf(map[string]any{"op": "read", "path": display(pth), "decision": "deny", "code": v.Code})})
		return acp.ReadTextFileResponse{}, denied(v.Code)
	}
	// Right before the read: the path must still lead where the chain
	// looked, and the file opened must be the file it classified.
	if !h.unchanged(p.Path, pth, checked) {
		v = deny(HandlerBoundary, VerdictChanged)
		h.audit(r, v, "")
		h.record(ctx, Item{Kind: ItemFile, Data: dataOf(map[string]any{"op": "read", "path": display(pth), "decision": "deny", "code": v.Code})})
		return acp.ReadTextFileResponse{}, denied(v.Code)
	}
	content, size, err := h.files.readChecked(pth.Rel, pth.info, p.Line, p.Limit)
	if err != nil {
		code := fsCode(err)
		if code == VerdictNotFound {
			return acp.ReadTextFileResponse{}, notFound()
		}
		return acp.ReadTextFileResponse{}, denied(code)
	}
	h.mu.Lock()
	h.sess.Counters.BytesRead += size
	h.mu.Unlock()
	// Only the path and size are stored, never the content.
	h.record(ctx, Item{Kind: ItemFile, Data: dataOf(map[string]any{"op": "read", "path": pth.Rel, "size": size, "decision": "allow", "code": v.Code})})
	return acp.ReadTextFileResponse{Content: content}, nil
}

func (h *host) WriteTextFile(ctx context.Context, p acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	if h.wrongSession(p.SessionID) {
		return acp.WriteTextFileResponse{}, denied(VerdictWrongSession)
	}
	if h.isCancelling() {
		return acp.WriteTextFileResponse{}, cancelledErr()
	}
	if len(p.Content) > MaxFileBytes {
		return acp.WriteTextFileResponse{}, denied(VerdictTooLarge)
	}
	pth, code := h.w.resolve(p.Path)
	checked, _ := h.w.forms(pth)
	r := &Request{Session: h.id, Kind: KindWrite, Paths: []Path{pth}, Size: int64(len(p.Content))}
	// The preview a person sees, taken before the chain: the current
	// content and the exact bytes that will be written.
	var before writeBefore
	if code == "" && h.files != nil {
		before.exists, before.sum, before.content, before.ok = h.files.state(pth.Rel)
		ctx = withPreview(ctx, previewOf(display(pth), before, p.Content))
	}
	v := h.decide(ctx, r)
	if v.Handler == HandlerCancel || ctx.Err() != nil {
		return acp.WriteTextFileResponse{}, cancelledErr()
	}
	if v.Decision == Allow {
		// The file must still be what the preview showed.
		exists, sum, _, ok := h.files.state(pth.Rel)
		if !before.ok || !ok || exists != before.exists || sum != before.sum {
			v = deny(HandlerBoundary, VerdictChanged)
			h.audit(r, v, "")
		}
	}
	if v.Decision != Allow {
		h.record(ctx, Item{Kind: ItemFile, Data: dataOf(map[string]any{"op": "write", "path": display(pth), "decision": "deny", "code": v.Code})})
		return acp.WriteTextFileResponse{}, denied(v.Code)
	}
	// Checked again right before the rename: the path must still lead
	// where the chain looked.
	sum, err := h.files.writeChecked(pth.Rel, p.Content, pth.dir, func() error {
		if !h.unchanged(p.Path, pth, checked) {
			return &fsError{VerdictChanged}
		}
		return nil
	})
	if err != nil {
		if fsCode(err) == VerdictChanged {
			h.audit(r, deny(HandlerBoundary, VerdictChanged), "")
		}
		return acp.WriteTextFileResponse{}, denied(fsCode(err))
	}
	h.mu.Lock()
	h.sess.Counters.BytesWritten += int64(len(p.Content))
	h.mu.Unlock()
	h.record(ctx, Item{Kind: ItemFile, Data: dataOf(map[string]any{"op": "write", "path": pth.Rel, "size": len(p.Content), "sha256": sum, "decision": "allow", "code": v.Code})})
	return acp.WriteTextFileResponse{}, nil
}

// unchanged reports whether asked still resolves as it did when the chain
// decided: the boundary passes, the resolved file is the same, and its
// forms (as spelled and as resolved) are the ones classified.
func (h *host) unchanged(asked string, before Path, checked []string) bool {
	now, code := h.w.resolve(asked)
	if code != "" || now.Real != before.Real {
		return false
	}
	forms, outside := h.w.forms(now)
	return !outside && slices.Equal(forms, checked)
}

// ---- terminal/* ----

func (h *host) CreateTerminal(ctx context.Context, p acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	if h.wrongSession(p.SessionID) {
		return acp.CreateTerminalResponse{}, denied(VerdictWrongSession)
	}
	if h.isCancelling() {
		return acp.CreateTerminalResponse{}, cancelledErr()
	}
	h.terms.mu.Lock()
	started := h.terms.started
	h.terms.mu.Unlock()
	if started >= MaxTerminals {
		return acp.CreateTerminalResponse{}, denied(VerdictTooManyTerminals)
	}
	cwdAbs := h.w.real
	if p.Cwd != nil {
		cwdAbs = *p.Cwd
	}
	cwd := Path{Abs: filepath.Clean(cwdAbs)}
	if filepath.IsAbs(cwdAbs) {
		if rel, ok := relOf(h.w.real, filepath.Clean(cwdAbs)); ok {
			cwd.Rel = rel
		}
		if real, err := filepath.EvalSymlinks(cwdAbs); err == nil {
			cwd.Real, cwd.Exists = real, true
		}
	}
	args := append([]string{}, p.Args...)
	if len(args) > 256 {
		return acp.CreateTerminalResponse{}, denied(VerdictProgram)
	}
	cmd := &Command{Program: p.Command, Args: args, Cwd: cwd, Line: quoteLine(p.Command, args)}
	for _, e := range p.Env {
		cmd.EnvNames = append(cmd.EnvNames, Visible(e.Name, 256))
	}
	if filepath.IsAbs(cwdAbs) {
		if resolved, ok := lookProgram(p.Command, cwdAbs, h.env.PATH); ok {
			cmd.Resolved = resolved
		}
	}
	r := &Request{Session: h.id, Kind: KindTerminal, Command: cmd}
	v := h.decide(withTerminalEnv(ctx, p.Env), r)
	if v.Handler == HandlerCancel || ctx.Err() != nil {
		return acp.CreateTerminalResponse{}, cancelledErr()
	}
	if v.Decision != Allow {
		h.record(ctx, Item{Kind: ItemTerminal, Data: dataOf(map[string]any{"line": Visible(cmd.Line, 4096), "cwd": Visible(cwd.Rel, 1024), "decision": "deny", "code": v.Code})})
		return acp.CreateTerminalResponse{}, denied(v.Code)
	}
	// After the approval (a person may have taken minutes): the cwd must
	// still resolve where it did, inside the worktree, and the program to
	// the same file.
	if !h.terminalUnchanged(cmd, cwdAbs) {
		v = deny(HandlerBoundary, VerdictChanged)
		h.audit(r, v, "")
		h.record(ctx, Item{Kind: ItemTerminal, Data: dataOf(map[string]any{"line": Visible(cmd.Line, 4096), "cwd": Visible(cwd.Rel, 1024), "decision": "deny", "code": v.Code})})
		return acp.CreateTerminalResponse{}, denied(v.Code)
	}
	env := append([]string{}, h.env.Terminal...)
	for _, e := range p.Env {
		env = append(env, e.Name+"="+e.Value)
	}
	var limit uint64
	if p.OutputByteLimit != nil {
		limit = *p.OutputByteLimit
	}
	t, err := h.terms.start(cmd, env, limit)
	if err != nil {
		if errors.Is(err, errTooManyTerminals) {
			return acp.CreateTerminalResponse{}, denied(VerdictTooManyTerminals)
		}
		return acp.CreateTerminalResponse{}, denied(VerdictProgram)
	}
	if t.cmd.Process != nil {
		addTerminalProc(h.st.path(h.id, terminalsFile), t.cmd.Process.Pid)
	}
	h.mu.Lock()
	h.sess.Counters.Terminals++
	h.mu.Unlock()
	h.record(ctx, Item{Kind: ItemTerminal, Data: dataOf(map[string]any{"id": t.id, "line": Visible(cmd.Line, 4096), "cwd": Visible(cwd.Rel, 1024), "decision": "allow", "code": v.Code})})
	return acp.CreateTerminalResponse{TerminalID: t.id}, nil
}

// terminalUnchanged re-resolves a terminal's cwd and program right before
// it starts.
func (h *host) terminalUnchanged(cmd *Command, cwdAbs string) bool {
	real, err := filepath.EvalSymlinks(cwdAbs)
	if err != nil || real != cmd.Cwd.Real {
		return false
	}
	if _, inside := relOf(h.w.real, real); !inside {
		return false
	}
	if rel, ok := relOf(h.w.real, real); ok && rel != "" {
		if _, code := h.w.resolve(real); code != "" && code != VerdictNotRegular {
			return false
		}
	}
	resolved, ok := lookProgram(cmd.Program, cwdAbs, h.env.PATH)
	return ok && resolved == cmd.Resolved
}

func (h *host) terminal(p acp.TerminalRequest) (*terminal, error) {
	if h.wrongSession(p.SessionID) {
		return nil, denied(VerdictWrongSession)
	}
	t, ok := h.terms.get(p.TerminalID)
	if !ok {
		return nil, denied(VerdictUnknownTerminal)
	}
	return t, nil
}

func (h *host) TerminalOutput(_ context.Context, p acp.TerminalRequest) (acp.TerminalOutputResponse, error) {
	t, err := h.terminal(p)
	if err != nil {
		return acp.TerminalOutputResponse{}, err
	}
	h.touch()
	return t.output(), nil
}

func (h *host) WaitForTerminalExit(ctx context.Context, p acp.TerminalRequest) (acp.TerminalExitStatus, error) {
	t, err := h.terminal(p)
	if err != nil {
		return acp.TerminalExitStatus{}, err
	}
	st, err := t.waitExit(ctx)
	if err != nil {
		return acp.TerminalExitStatus{}, cancelledErr()
	}
	h.touch()
	h.recordTerminal(ctx, t)
	return st, nil
}

func (h *host) KillTerminal(_ context.Context, p acp.TerminalRequest) error {
	t, err := h.terminal(p)
	if err != nil {
		return err
	}
	t.kill()
	return nil
}

func (h *host) ReleaseTerminal(ctx context.Context, p acp.TerminalRequest) error {
	t, err := h.terminal(p)
	if err != nil {
		return err
	}
	h.terms.release(t.id)
	h.recordTerminal(ctx, t)
	return nil
}

// recordTerminal stores the command line and the last 64 KiB of output,
// terminal sequences removed.
func (h *host) recordTerminal(ctx context.Context, t *terminal) {
	out := t.output()
	data := map[string]any{"id": t.id, "line": Visible(t.line, 4096), "cwd": Visible(t.cwd, 1024), "output": t.shown()}
	if st := out.ExitStatus; st != nil {
		if st.ExitCode != nil {
			data["exit"] = int(*st.ExitCode)
		}
		if st.Signal != nil {
			data["signal"] = SanitizeLine(*st.Signal, 32)
		}
	}
	h.record(ctx, Item{Kind: ItemTerminal, Data: dataOf(data)})
}

var _ acp.ClientHandler = (*host)(nil)

// writeBefore is a write target's state when its preview was taken.
type writeBefore struct {
	exists, ok bool
	sum        string
	content    []byte
}

// preview is an fs_write ask's diff and content hash.
type preview struct {
	diff      AskDiff
	newSHA256 string
}

type previewKey struct{}

func withPreview(ctx context.Context, p preview) context.Context {
	return context.WithValue(ctx, previewKey{}, p)
}

func previewFrom(ctx context.Context) (preview, bool) {
	p, ok := ctx.Value(previewKey{}).(preview)
	return p, ok
}

// previewOf builds the preview: not redacted, each text at most
// MaxPreview; cut when longer or not valid UTF-8.
func previewOf(where string, before writeBefore, content string) preview {
	sum := sha256.Sum256([]byte(content))
	d := AskDiff{Path: where}
	if !before.ok || !utf8.Valid(before.content) || !utf8.ValidString(content) || len(before.content) > MaxPreview || len(content) > MaxPreview {
		d.Cut = true
	}
	if !d.Cut {
		d.OldText, d.NewText = string(before.content), content
	}
	return preview{diff: d, newSHA256: hex.EncodeToString(sum[:])}
}
