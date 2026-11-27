package acp

import (
	"bytes"
	"encoding/json"
)

// Decode rules for phase 7 (ADR "Phase 7 ACP client", the phase 6 rule
// kept for every ACP message): unknown fields are ignored; an optional
// field that fails to decode takes its default ("default on error"); an
// invalid list item is dropped ("skip invalid items"). A required field
// that is missing or has the wrong type makes the whole message invalid:
// an answer is then ErrProtocol, an agent request -32602, and a
// session/update is dropped. A session/update whose kind is unknown is
// never invalid: it keeps Kind and Raw.
//
// Two places are stricter than "skip invalid items", because dropping an
// item would change what runs: a terminal/create whose args or env holds
// an invalid item is invalid as a whole.

// maxRaw bounds SessionUpdate.Raw and ToolCall.RawInput; a larger value is
// dropped.
const maxRaw = 64 << 10

// protocolError is an answer that is not an ACP result. It matches
// ErrProtocol with errors.Is.
type protocolError struct{ msg string }

func (e protocolError) Error() string { return "acp: " + e.msg }
func (e protocolError) Unwrap() error { return ErrProtocol }

type fields map[string]json.RawMessage

// object decodes raw as a JSON object; null and anything else fail.
func object(raw json.RawMessage) (fields, bool) {
	var f fields
	if len(raw) == 0 || json.Unmarshal(raw, &f) != nil || f == nil {
		return nil, false
	}
	return f, true
}

// str is a present string field.
func (f fields) str(key string) (string, bool) {
	v, ok := f[key]
	if !ok {
		return "", false
	}
	var s string
	if t := bytes.TrimSpace(v); len(t) == 0 || t[0] != '"' || json.Unmarshal(v, &s) != nil {
		return "", false
	}
	return s, true
}

// need is a present, non-empty string field.
func (f fields) need(key string) (string, bool) {
	s, ok := f.str(key)
	return s, ok && s != ""
}

// opt is a string field, "" on error.
func (f fields) opt(key string) string {
	s, _ := f.str(key)
	return s
}

// optPtr is a string field, nil when absent, null or of the wrong type.
func (f fields) optPtr(key string) *string {
	if s, ok := f.str(key); ok {
		return &s
	}
	return nil
}

func (f fields) uint32Ptr(key string) *uint32 {
	v, ok := f[key]
	if !ok || isNull(v) {
		return nil
	}
	var n uint32
	if json.Unmarshal(v, &n) != nil {
		return nil
	}
	return &n
}

func (f fields) uint64Ptr(key string) *uint64 {
	v, ok := f[key]
	if !ok || isNull(v) {
		return nil
	}
	var n uint64
	if json.Unmarshal(v, &n) != nil {
		return nil
	}
	return &n
}

// list is an array field's items; nil when absent or not an array.
func (f fields) list(key string) ([]json.RawMessage, bool) {
	v, ok := f[key]
	if !ok {
		return nil, false
	}
	var items []json.RawMessage
	if json.Unmarshal(v, &items) != nil || items == nil {
		return nil, false
	}
	return items, true
}

// raw is a field kept as JSON, nil when absent, null or over maxRaw.
func (f fields) raw(key string) json.RawMessage {
	v, ok := f[key]
	if !ok || isNull(v) || len(v) > maxRaw {
		return nil
	}
	return append(json.RawMessage(nil), v...)
}

func decodeNewSession(raw json.RawMessage) (NewSessionResponse, error) {
	f, ok := object(raw)
	if !ok {
		return NewSessionResponse{}, protocolError{"the session/new answer is not an object"}
	}
	id, ok := f.need("sessionId")
	if !ok {
		return NewSessionResponse{}, protocolError{"the session/new answer has no sessionId"}
	}
	return NewSessionResponse{SessionID: id, Modes: decodeModes(f["modes"])}, nil
}

func decodeModes(raw json.RawMessage) *SessionModeState {
	f, ok := object(raw)
	if !ok {
		return nil
	}
	cur, ok := f.need("currentModeId")
	if !ok {
		return nil
	}
	items, ok := f.list("availableModes")
	if !ok {
		return nil
	}
	out := &SessionModeState{CurrentModeID: cur, AvailableModes: []SessionMode{}}
	for _, item := range items {
		m, ok := object(item)
		if !ok {
			continue
		}
		id, ok1 := m.need("id")
		name, ok2 := m.str("name")
		if !ok1 || !ok2 {
			continue
		}
		out.AvailableModes = append(out.AvailableModes, SessionMode{ID: id, Name: name, Description: m.opt("description")})
	}
	return out
}

func decodePrompt(raw json.RawMessage) (PromptResponse, error) {
	f, ok := object(raw)
	if !ok {
		return PromptResponse{}, protocolError{"the session/prompt answer is not an object"}
	}
	r, ok := f.need("stopReason")
	if !ok {
		return PromptResponse{}, protocolError{"the session/prompt answer has no stopReason"}
	}
	return PromptResponse{StopReason: StopReason(r)}, nil
}

// decodeSessionNotification decodes session/update params. It reports
// false for an invalid notification, which is dropped.
func decodeSessionNotification(raw json.RawMessage) (SessionNotification, bool) {
	f, ok := object(raw)
	if !ok {
		return SessionNotification{}, false
	}
	id, ok := f.need("sessionId")
	if !ok {
		return SessionNotification{}, false
	}
	u, ok := decodeUpdate(f["update"])
	if !ok {
		return SessionNotification{}, false
	}
	return SessionNotification{SessionID: id, Update: u}, true
}

// decodeUpdate decodes one SessionUpdate. An unknown kind keeps Kind and
// Raw. A known kind whose required fields are invalid is invalid (false).
// available_commands_update and config_option_update keep only Kind: no
// field of SessionUpdate holds them.
func decodeUpdate(raw json.RawMessage) (SessionUpdate, bool) {
	f, ok := object(raw)
	if !ok {
		return SessionUpdate{}, false
	}
	kind, ok := f.need("sessionUpdate")
	if !ok {
		return SessionUpdate{}, false
	}
	u := SessionUpdate{Kind: UpdateKind(kind)}
	switch u.Kind {
	case UpdateUserMessageChunk, UpdateAgentMessageChunk, UpdateAgentThoughtChunk:
		c, ok := decodeContentBlock(f["content"])
		if !ok {
			return SessionUpdate{}, false
		}
		u.Content = c
		u.MessageID = f.opt("messageId")
	case UpdateToolCall:
		tc, ok := decodeToolCall(f)
		if !ok {
			return SessionUpdate{}, false
		}
		u.ToolCall = tc
	case UpdateToolCallUpdate:
		tu, ok := decodeToolCallUpdate(f)
		if !ok {
			return SessionUpdate{}, false
		}
		u.ToolCallUpdate = tu
	case UpdatePlan:
		p, ok := decodePlan(f)
		if !ok {
			return SessionUpdate{}, false
		}
		u.Plan = p
	case UpdateCurrentMode:
		m, ok := f.need("currentModeId")
		if !ok {
			return SessionUpdate{}, false
		}
		u.CurrentModeID = m
	case UpdateSessionInfo:
		u.Title = f.opt("title")
	case UpdateUsage:
		us, ok := decodeUsage(f)
		if !ok {
			return SessionUpdate{}, false
		}
		u.Usage = us
	case UpdateAvailableCommands, UpdateConfigOption:
	default:
		if len(raw) <= maxRaw {
			u.Raw = append(json.RawMessage(nil), raw...)
		}
	}
	return u, true
}

// decodeContentBlock keeps text and the names of other blocks: image and
// audio data and embedded resource contents are dropped. An unknown type
// keeps only Type.
func decodeContentBlock(raw json.RawMessage) (*ContentBlock, bool) {
	f, ok := object(raw)
	if !ok {
		return nil, false
	}
	t, ok := f.need("type")
	if !ok {
		return nil, false
	}
	b := &ContentBlock{Type: t}
	switch t {
	case "text":
		text, ok := f.str("text")
		if !ok {
			return nil, false
		}
		b.Text = text
	case "image", "audio":
		b.MimeType = f.opt("mimeType")
	case "resource_link":
		uri, ok1 := f.need("uri")
		name, ok2 := f.str("name")
		if !ok1 || !ok2 {
			return nil, false
		}
		b.URI, b.Name, b.MimeType = uri, name, f.opt("mimeType")
	case "resource":
		res, ok := object(f["resource"])
		if !ok {
			return nil, false
		}
		uri, ok := res.need("uri")
		if !ok {
			return nil, false
		}
		b.URI = uri
	}
	return b, true
}

// toolKind maps a kind to a ToolKind; an unknown one is ToolOther.
func toolKind(s string) ToolKind {
	switch k := ToolKind(s); k {
	case ToolRead, ToolEdit, ToolDelete, ToolMove, ToolSearch, ToolExecute, ToolThink, ToolFetch, ToolSwitchMode, ToolOther:
		return k
	}
	return ToolOther
}

func toolStatus(s string) (ToolCallStatus, bool) {
	switch st := ToolCallStatus(s); st {
	case ToolPending, ToolInProgress, ToolCompleted, ToolFailed:
		return st, true
	}
	return "", false
}

func decodeToolCall(f fields) (*ToolCall, bool) {
	id, ok1 := f.need("toolCallId")
	title, ok2 := f.str("title")
	if !ok1 || !ok2 {
		return nil, false
	}
	tc := &ToolCall{ToolCallID: id, Title: title, Kind: ToolOther, Status: ToolPending}
	if k, ok := f.str("kind"); ok {
		tc.Kind = toolKind(k)
	}
	if s, ok := f.str("status"); ok {
		if st, ok := toolStatus(s); ok {
			tc.Status = st
		}
	}
	tc.Content = decodeToolContent(f)
	tc.Locations = decodeLocations(f)
	tc.RawInput = f.raw("rawInput")
	return tc, true
}

func decodeToolCallUpdate(f fields) (*ToolCallUpdate, bool) {
	id, ok := f.need("toolCallId")
	if !ok {
		return nil, false
	}
	tu := &ToolCallUpdate{ToolCallID: id, Title: f.optPtr("title")}
	if k, ok := f.str("kind"); ok {
		kind := toolKind(k)
		tu.Kind = &kind
	}
	if s, ok := f.str("status"); ok {
		if st, ok := toolStatus(s); ok {
			tu.Status = &st
		}
	}
	tu.Content = decodeToolContent(f)
	tu.Locations = decodeLocations(f)
	tu.RawInput = f.raw("rawInput")
	return tu, true
}

// decodeToolContent keeps "content", "diff" and "terminal" items; "session"
// and unknown types are skipped, like invalid items.
func decodeToolContent(f fields) []ToolCallContent {
	items, ok := f.list("content")
	if !ok {
		return nil
	}
	var out []ToolCallContent
	for _, item := range items {
		c, ok := object(item)
		if !ok {
			continue
		}
		t, _ := c.str("type")
		switch t {
		case "content":
			b, ok := decodeContentBlock(c["content"])
			if !ok {
				continue
			}
			out = append(out, ToolCallContent{Type: t, Content: b})
		case "diff":
			path, ok1 := c.need("path")
			newText, ok2 := c.str("newText")
			if !ok1 || !ok2 {
				continue
			}
			out = append(out, ToolCallContent{Type: t, Path: path, OldText: c.optPtr("oldText"), NewText: newText})
		case "terminal":
			id, ok := c.need("terminalId")
			if !ok {
				continue
			}
			out = append(out, ToolCallContent{Type: t, TerminalID: id})
		}
	}
	return out
}

func decodeLocations(f fields) []ToolCallLocation {
	items, ok := f.list("locations")
	if !ok {
		return nil
	}
	var out []ToolCallLocation
	for _, item := range items {
		l, ok := object(item)
		if !ok {
			continue
		}
		path, ok := l.need("path")
		if !ok {
			continue
		}
		out = append(out, ToolCallLocation{Path: path, Line: l.uint32Ptr("line")})
	}
	return out
}

func decodePlan(f fields) (*Plan, bool) {
	items, ok := f.list("entries")
	if !ok {
		return nil, false
	}
	p := &Plan{Entries: []PlanEntry{}}
	for _, item := range items {
		e, ok := object(item)
		if !ok {
			continue
		}
		content, ok1 := e.str("content")
		prio, ok2 := e.str("priority")
		status, ok3 := e.str("status")
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		switch prio {
		case "high", "medium", "low":
		default:
			continue
		}
		switch status {
		case "pending", "in_progress", "completed":
		default:
			continue
		}
		p.Entries = append(p.Entries, PlanEntry{Content: content, Priority: prio, Status: status})
	}
	return p, true
}

func decodeUsage(f fields) (*UsageUpdate, bool) {
	used, size := f.uint64Ptr("used"), f.uint64Ptr("size")
	if used == nil || size == nil {
		return nil, false
	}
	u := &UsageUpdate{Used: *used, Size: *size}
	if c, ok := object(f["cost"]); ok {
		var amount float64
		cur, ok1 := c.need("currency")
		if v, ok := c["amount"]; ok1 && ok && json.Unmarshal(v, &amount) == nil {
			u.Cost = &Cost{Amount: amount, Currency: cur}
		}
	}
	return u, true
}

// Agent requests. Each reports false when a required field is missing or
// has the wrong type; the request is then answered -32602.

func decodePermissionRequest(raw json.RawMessage) (RequestPermissionRequest, bool) {
	f, ok := object(raw)
	if !ok {
		return RequestPermissionRequest{}, false
	}
	sid, ok := f.need("sessionId")
	if !ok {
		return RequestPermissionRequest{}, false
	}
	tf, ok := object(f["toolCall"])
	if !ok {
		return RequestPermissionRequest{}, false
	}
	tu, ok := decodeToolCallUpdate(tf)
	if !ok {
		return RequestPermissionRequest{}, false
	}
	items, ok := f.list("options")
	if !ok {
		return RequestPermissionRequest{}, false
	}
	r := RequestPermissionRequest{SessionID: sid, ToolCall: *tu, Options: []PermissionOption{}}
	for _, item := range items {
		o, ok := object(item)
		if !ok {
			continue
		}
		id, ok1 := o.need("optionId")
		name, ok2 := o.str("name")
		kind, ok3 := o.str("kind")
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		switch k := PermissionOptionKind(kind); k {
		case AllowOnce, AllowAlways, RejectOnce, RejectAlways:
			r.Options = append(r.Options, PermissionOption{OptionID: id, Name: name, Kind: k})
		}
	}
	return r, true
}

func decodeReadRequest(raw json.RawMessage) (ReadTextFileRequest, bool) {
	f, ok := object(raw)
	if !ok {
		return ReadTextFileRequest{}, false
	}
	sid, ok1 := f.need("sessionId")
	path, ok2 := f.need("path")
	if !ok1 || !ok2 {
		return ReadTextFileRequest{}, false
	}
	return ReadTextFileRequest{SessionID: sid, Path: path, Line: f.uint32Ptr("line"), Limit: f.uint32Ptr("limit")}, true
}

func decodeWriteRequest(raw json.RawMessage) (WriteTextFileRequest, bool) {
	f, ok := object(raw)
	if !ok {
		return WriteTextFileRequest{}, false
	}
	sid, ok1 := f.need("sessionId")
	path, ok2 := f.need("path")
	content, ok3 := f.str("content")
	if !ok1 || !ok2 || !ok3 {
		return WriteTextFileRequest{}, false
	}
	return WriteTextFileRequest{SessionID: sid, Path: path, Content: content}, true
}

func decodeCreateTerminal(raw json.RawMessage) (CreateTerminalRequest, bool) {
	f, ok := object(raw)
	if !ok {
		return CreateTerminalRequest{}, false
	}
	sid, ok1 := f.need("sessionId")
	cmd, ok2 := f.need("command")
	if !ok1 || !ok2 {
		return CreateTerminalRequest{}, false
	}
	r := CreateTerminalRequest{SessionID: sid, Command: cmd, OutputByteLimit: f.uint64Ptr("outputByteLimit")}
	if v, ok := f["args"]; ok && !isNull(v) {
		items, ok := f.list("args")
		if !ok {
			return CreateTerminalRequest{}, false
		}
		for _, item := range items {
			// A null item would decode as "" without an error.
			a, ok := fields{"a": item}.str("a")
			if !ok {
				return CreateTerminalRequest{}, false
			}
			r.Args = append(r.Args, a)
		}
	}
	if v, ok := f["env"]; ok && !isNull(v) {
		items, ok := f.list("env")
		if !ok {
			return CreateTerminalRequest{}, false
		}
		for _, item := range items {
			e, ok := object(item)
			if !ok {
				return CreateTerminalRequest{}, false
			}
			name, ok1 := e.need("name")
			value, ok2 := e.str("value")
			if !ok1 || !ok2 {
				return CreateTerminalRequest{}, false
			}
			r.Env = append(r.Env, EnvVariable{Name: name, Value: value})
		}
	}
	if v, ok := f["cwd"]; ok && !isNull(v) {
		cwd, ok := f.str("cwd")
		if !ok {
			return CreateTerminalRequest{}, false
		}
		r.Cwd = &cwd
	}
	return r, true
}

func decodeTerminalRequest(raw json.RawMessage) (TerminalRequest, bool) {
	f, ok := object(raw)
	if !ok {
		return TerminalRequest{}, false
	}
	sid, ok1 := f.need("sessionId")
	id, ok2 := f.need("terminalId")
	if !ok1 || !ok2 {
		return TerminalRequest{}, false
	}
	return TerminalRequest{SessionID: sid, TerminalID: id}, true
}

// decodeCancelRequest returns the requestId of "$/cancel_request": a
// number, a string or null, compacted.
func decodeCancelRequest(raw json.RawMessage) (json.RawMessage, bool) {
	f, ok := object(raw)
	if !ok {
		return nil, false
	}
	v, ok := f["requestId"]
	if !ok {
		return nil, false
	}
	var buf bytes.Buffer
	if json.Compact(&buf, v) != nil {
		return nil, false
	}
	switch t := buf.Bytes(); {
	case len(t) == 0:
		return nil, false
	case t[0] == '"', t[0] == '-', t[0] >= '0' && t[0] <= '9', string(t) == "null":
		return json.RawMessage(t), true
	}
	return nil, false
}
