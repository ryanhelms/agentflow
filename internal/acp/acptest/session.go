package acptest

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
)

// SessionScript is what the fake agent does in a session ("Phase 7 fake
// ACP agent" in the ADR): it answers session/new, session/set_mode and
// session/cancel, and plays Steps after session/prompt, in order.
//
// Strings in update, ask and tool_unasked JSON may hold placeholders,
// replaced before sending: {cwd} is the cwd of session/new, {session} the
// AGENTFLOW_ACP_SESSION variable the fake was started with ("unknown" when
// unset), and {terminal} the ID of the last terminal/create answer. An ask
// without a sessionId gets the session's; a wait or release without a
// terminalId gets {terminal}, and is skipped when no terminal was created.
type SessionScript struct {
	// SessionID is the session/new answer; empty means DefaultSessionID.
	SessionID string `json:"session_id,omitempty"`
	// Modes, when set, is sent in the session/new answer.
	Modes *acp.SessionModeState `json:"modes,omitempty"`
	Steps []Step                `json:"steps"`
}

// DefaultSessionID is the sessionId the fake answers session/new with.
const DefaultSessionID = "fake-session-1"

// Step is one thing the fake does after session/prompt. Exactly one field
// is set.
type Step struct {
	// Update is sent as a session/update: the "update" object, as JSON.
	Update json.RawMessage `json:"update,omitempty"`
	// Ask sends one client request and logs the answer or the error code.
	Ask *Ask `json:"ask,omitempty"`
	// ToolUnasked is a tool_call (the ToolCall object, as JSON) sent as
	// in_progress and then updated to completed, with no request.
	ToolUnasked json.RawMessage `json:"tool_unasked,omitempty"`
	// Sleep waits, e.g. "100ms"; session/cancel ends the wait.
	Sleep string `json:"sleep,omitempty"`
	// Stop answers session/prompt with this stop reason and ends the turn.
	Stop acp.StopReason `json:"stop,omitempty"`
	// Crash exits the fake (code 4) without answering.
	Crash bool `json:"crash,omitempty"`
	// NewSessionError, in any step, makes session/new fail with this code.
	NewSessionError int `json:"new_session_error,omitempty"`
	// Draft plays a draft answer for the feature named on the prompt's
	// first line (phase 8, draft.go).
	Draft *DraftStep `json:"draft,omitempty"`
}

// Ask is one client request. Exactly one field is set, holding the
// request's params as JSON.
type Ask struct {
	Permission json.RawMessage `json:"permission,omitempty"`
	Read       json.RawMessage `json:"read,omitempty"`
	Write      json.RawMessage `json:"write,omitempty"`
	Terminal   json.RawMessage `json:"terminal,omitempty"`
	Wait       json.RawMessage `json:"wait,omitempty"`
	Release    json.RawMessage `json:"release,omitempty"`
}

func (a *Ask) method() (string, json.RawMessage) {
	switch {
	case a.Permission != nil:
		return acp.MethodRequestPermission, a.Permission
	case a.Read != nil:
		return acp.MethodReadTextFile, a.Read
	case a.Write != nil:
		return acp.MethodWriteTextFile, a.Write
	case a.Terminal != nil:
		return acp.MethodTerminalCreate, a.Terminal
	case a.Wait != nil:
		return acp.MethodTerminalWaitForExit, a.Wait
	case a.Release != nil:
		return acp.MethodTerminalRelease, a.Release
	}
	return "", nil
}

// JSON marshals v for a Step or Ask field. A value that does not marshal
// (a bug in the test) becomes null.
func JSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return b
}

// askTimeout bounds one ask: a held permission request waits for a person
// (the session's ask_timeout), so jsonrpc's default 10 s is far too short.
const askTimeout = 24 * time.Hour

// sessionLimits are the fake's jsonrpc limits in a session: the frames and
// in-flight count of acp.SessionLimits, and no practical call timeout.
var sessionLimits = jsonrpc.Limits{
	MaxFrame:    acp.SessionLimits.MaxFrame,
	MaxInFlight: acp.SessionLimits.MaxInFlight,
	CallTimeout: askTimeout,
}

// player plays a SessionScript on one connection.
type player struct {
	s    *SessionScript
	lg   *logger
	conn func() jsonrpc.Conn

	mu        sync.Mutex
	cwd       string
	terminal  string
	prompt    string // the first text block of session/prompt
	cancelled chan struct{}
	cancelOne sync.Once
}

func newPlayer(s *SessionScript, lg *logger, conn func() jsonrpc.Conn) *player {
	return &player{s: s, lg: lg, conn: conn, cancelled: make(chan struct{})}
}

func (p *player) sessionID() string {
	if p.s.SessionID != "" {
		return p.s.SessionID
	}
	return DefaultSessionID
}

// handle answers a session method; ok is false for any other method.
func (p *player) handle(ctx context.Context, r *jsonrpc.Request) (result any, ok bool, err error) {
	switch r.Method {
	case acp.MethodSessionNew:
		for _, st := range p.s.Steps {
			if st.NewSessionError != 0 {
				msg := "session/new failed"
				if st.NewSessionError == acp.CodeAuthRequired {
					msg = "Authentication required"
				}
				return nil, true, &jsonrpc.Error{Code: st.NewSessionError, Message: msg}
			}
		}
		var req struct {
			Cwd string `json:"cwd"`
		}
		_ = json.Unmarshal(r.Params, &req)
		p.mu.Lock()
		p.cwd = req.Cwd
		p.mu.Unlock()
		return acp.NewSessionResponse{SessionID: p.sessionID(), Modes: p.s.Modes}, true, nil
	case acp.MethodSessionSetMode:
		return struct{}{}, true, nil
	case acp.MethodSessionCancel:
		p.cancelOne.Do(func() { close(p.cancelled) })
		return nil, true, nil
	case acp.MethodSessionPrompt:
		var req struct {
			Prompt []acp.ContentBlock `json:"prompt"`
		}
		_ = json.Unmarshal(r.Params, &req)
		p.mu.Lock()
		p.prompt = ""
		for _, b := range req.Prompt {
			if b.Type == "text" {
				p.prompt = b.Text
				break
			}
		}
		p.mu.Unlock()
		return p.play(ctx), true, nil
	}
	return nil, false, nil
}

func (p *player) isCancelled() bool {
	select {
	case <-p.cancelled:
		return true
	default:
		return false
	}
}

// play runs the steps and returns the session/prompt answer.
func (p *player) play(ctx context.Context) acp.PromptResponse {
	stop := func(r acp.StopReason) acp.PromptResponse {
		if p.isCancelled() {
			r = acp.StopCancelled
		}
		return acp.PromptResponse{StopReason: r}
	}
	for _, st := range p.s.Steps {
		if p.isCancelled() || ctx.Err() != nil {
			return stop(acp.StopCancelled)
		}
		switch {
		case st.Update != nil:
			p.notifyUpdate(ctx, p.fill(st.Update))
		case st.Ask != nil:
			p.ask(ctx, st.Ask)
		case st.ToolUnasked != nil:
			p.toolUnasked(ctx, p.fill(st.ToolUnasked))
		case st.Sleep != "":
			d, err := time.ParseDuration(st.Sleep)
			if err != nil {
				continue
			}
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-p.cancelled:
			case <-ctx.Done():
			}
			t.Stop()
		case st.Draft != nil:
			p.mu.Lock()
			prompt := p.prompt
			p.mu.Unlock()
			for _, c := range DraftChunks(DraftAnswer(st.Draft.Answers, prompt)) {
				p.notifyUpdate(ctx, chunk(c).Update)
			}
		case st.Stop != "":
			return stop(st.Stop)
		case st.Crash:
			os.Exit(4)
		}
	}
	return stop(acp.StopEndTurn)
}

func (p *player) notifyUpdate(ctx context.Context, update json.RawMessage) {
	_ = p.conn().Notify(ctx, acp.MethodSessionUpdate, map[string]any{"sessionId": p.sessionID(), "update": update})
}

func (p *player) toolUnasked(ctx context.Context, call json.RawMessage) {
	var tc map[string]json.RawMessage
	if json.Unmarshal(call, &tc) != nil || tc == nil {
		return
	}
	tc["sessionUpdate"] = JSON(string(acp.UpdateToolCall))
	tc["status"] = JSON(string(acp.ToolInProgress))
	p.notifyUpdate(ctx, JSON(tc))
	done := map[string]json.RawMessage{
		"sessionUpdate": JSON(string(acp.UpdateToolCallUpdate)),
		"toolCallId":    tc["toolCallId"],
		"status":        JSON(string(acp.ToolCompleted)),
	}
	p.notifyUpdate(ctx, JSON(done))
}

func (p *player) ask(ctx context.Context, a *Ask) {
	method, params := a.method()
	if method == "" {
		return
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(p.fill(params), &m) != nil || m == nil {
		m = map[string]json.RawMessage{}
	}
	if _, ok := m["sessionId"]; !ok {
		m["sessionId"] = JSON(p.sessionID())
	}
	if method == acp.MethodTerminalWaitForExit || method == acp.MethodTerminalRelease {
		if _, ok := m["terminalId"]; !ok {
			p.mu.Lock()
			term := p.terminal
			p.mu.Unlock()
			if term == "" {
				p.lg.write(LogEntry{Asked: method, Skipped: true})
				return
			}
			m["terminalId"] = JSON(term)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var answer json.RawMessage
	err := p.conn().Call(ctx, method, m, &answer)
	e := LogEntry{Asked: method}
	var rerr *jsonrpc.Error
	switch {
	case errors.As(err, &rerr):
		e.Code = rerr.Code
		var d jsonrpc.ErrorData
		if json.Unmarshal(rerr.Data, &d) == nil {
			e.ErrData = d.Code
		}
	case err != nil:
		e.Code = -1 // no answer: the connection closed or the call timed out
	default:
		e.Answer = answer
		if method == acp.MethodTerminalCreate {
			var t acp.CreateTerminalResponse
			if json.Unmarshal(answer, &t) == nil && t.TerminalID != "" {
				p.mu.Lock()
				p.terminal = t.TerminalID
				p.mu.Unlock()
			}
		}
	}
	p.lg.write(e)
}

// fill replaces the placeholders in raw's strings.
func (p *player) fill(raw json.RawMessage) json.RawMessage {
	p.mu.Lock()
	cwd, term := p.cwd, p.terminal
	p.mu.Unlock()
	session := os.Getenv("AGENTFLOW_ACP_SESSION")
	if session == "" {
		session = "unknown"
	}
	r := strings.NewReplacer("{cwd}", jsonInner(cwd), "{session}", jsonInner(session), "{terminal}", jsonInner(term))
	return json.RawMessage(r.Replace(string(raw)))
}

// jsonInner is s escaped for use inside a JSON string.
func jsonInner(s string) string {
	b, _ := json.Marshal(s)
	return string(b[1 : len(b)-1])
}
