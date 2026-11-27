package acp_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp"
)

// Examples copied from the ACP v1 docs at schema-v1.35.0 (commit
// 065b57951b6b2a91ac5eddd249e145d1f6d54545): every agent-to-client message
// in docs/protocol/v1/{session-setup,session-modes,prompt-turn,tool-calls,
// agent-plan,file-system,terminals}.mdx, by doc page and method. The
// schema itself (schema/v1/schema.json) has no examples.

type example struct {
	page, method, params string
}

var schemaExamples = []example{
	{"session-setup", "session/update", `{"sessionId":"sess_789xyz","update":{"sessionUpdate":"user_message_chunk","messageId":"msg_user_8f7a1","content":{"type":"text","text":"What's the capital of France?"}}}`},
	{"session-setup", "session/update", `{"sessionId":"sess_789xyz","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_agent_c42b9","content":{"type":"text","text":"The capital of France is Paris."}}}`},
	{"session-modes", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"current_mode_update","currentModeId":"code"}}`},
	{"session-modes", "session/request_permission", `{"sessionId":"sess_abc123def456","toolCall":{"toolCallId":"call_switch_mode_001","title":"Ready for implementation","kind":"switch_mode","status":"pending","content":[{"type":"content","content":{"type":"text","text":"## Implementation Plan..."}}]},"options":[{"optionId":"code","name":"Yes, and auto-accept all actions","kind":"allow_always"},{"optionId":"ask","name":"Yes, and manually accept actions","kind":"allow_once"},{"optionId":"reject","name":"No, stay in architect mode","kind":"reject_once"}]}`},
	{"prompt-turn", "session/update", `{"sessionId":"child_session","update":{"sessionUpdate":"session_message","messageId":"received_message_1","senderSessionId":"parent_session","recipientSessionId":"child_session"}}`},
	{"prompt-turn", "session/update", `{"sessionId":"child_session","update":{"sessionUpdate":"session_message_chunk","messageId":"received_message_1","content":{"type":"text","text":"Please investigate "}}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"plan","entries":[{"content":"Check for syntax errors","priority":"high","status":"pending"},{"content":"Identify potential type issues","priority":"medium","status":"pending"},{"content":"Review error handling patterns","priority":"medium","status":"pending"},{"content":"Suggest improvements","priority":"low","status":"pending"}]}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"agent_message_chunk","messageId":"msg_agent_c42b9","content":{"type":"text","text":"I'll analyze your code for potential issues. Let me examine it..."}}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"tool_call","toolCallId":"call_001","title":"Analyzing Python code","kind":"other","status":"pending"}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"notice","severity":"warning","title":"MCP server unavailable","description":"Continuing without it."}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"compaction_update","compactionId":"cmp_001","status":"in_progress"}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"compaction_summary_chunk","compactionId":"cmp_001","content":{"type":"text","text":"## Retained context\n\n"}}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"compaction_update","compactionId":"cmp_001","status":"completed","summary":[{"type":"text","text":"## Retained context\n\nThe user is updating the ACP schema."}]}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"usage_update","used":53000,"size":200000,"cost":{"amount":0.045,"currency":"USD"}}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"tool_call_update","toolCallId":"call_001","status":"in_progress"}}`},
	{"prompt-turn", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"tool_call_update","toolCallId":"call_001","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Analysis complete:\n- No syntax errors found\n- Consider adding type hints for better clarity\n- The function could benefit from error handling for empty lists"}}]}}`},
	{"tool-calls", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"tool_call","toolCallId":"call_001","title":"Reading configuration file","name":"read_file","kind":"read","status":"pending"}}`},
	{"tool-calls", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"tool_call_update","toolCallId":"call_001","status":"in_progress","content":[{"type":"content","content":{"type":"text","text":"Found 3 configuration files..."}}]}}`},
	{"tool-calls", "session/request_permission", `{"sessionId":"sess_abc123def456","toolCall":{"toolCallId":"call_001"},"options":[{"optionId":"allow-once","name":"Allow once","kind":"allow_once"},{"optionId":"reject-once","name":"Reject","kind":"reject_once"}]}`},
	{"agent-plan", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"plan","entries":[{"content":"Analyze the existing codebase structure","priority":"high","status":"pending"},{"content":"Identify components that need refactoring","priority":"high","status":"pending"},{"content":"Create unit tests for critical functions","priority":"medium","status":"pending"}]}}`},
	{"agent-plan", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"plan_update","plan":{"type":"items","planId":"plan-1","entries":[{"content":"Analyze the existing codebase structure","priority":"high","status":"pending"}]}}}`},
	{"agent-plan", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"plan_update","plan":{"type":"markdown","planId":"implementation-plan","content":"## Steps\n- [ ] Refactor module\n- [ ] Add tests","path":"/home/user/project/PLAN.md"}}}`},
	{"agent-plan", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"plan_removed","planId":"plan-1"}}`},
	{"file-system", "fs/read_text_file", `{"sessionId":"sess_abc123def456","path":"/home/user/project/src/main.py","line":10,"limit":50}`},
	{"file-system", "fs/write_text_file", `{"sessionId":"sess_abc123def456","path":"/home/user/project/config.json","content":"{\n  \"debug\": true,\n  \"version\": \"1.0.0\"\n}"}`},
	{"terminals", "terminal/create", `{"sessionId":"sess_abc123def456","command":"npm","args":["test","--coverage"],"env":[{"name":"NODE_ENV","value":"test"}],"cwd":"/home/user/project","outputByteLimit":1048576}`},
	{"terminals", "session/update", `{"sessionId":"sess_abc123def456","update":{"sessionUpdate":"tool_call","toolCallId":"call_002","title":"Running tests","kind":"execute","status":"in_progress","content":[{"type":"terminal","terminalId":"term_xyz789"}]}}`},
	{"terminals", "terminal/output", `{"sessionId":"sess_abc123def456","terminalId":"term_xyz789"}`},
	{"terminals", "terminal/wait_for_exit", `{"sessionId":"sess_abc123def456","terminalId":"term_xyz789"}`},
	{"terminals", "terminal/kill", `{"sessionId":"sess_abc123def456","terminalId":"term_xyz789"}`},
	{"terminals", "terminal/release", `{"sessionId":"sess_abc123def456","terminalId":"term_xyz789"}`},
}

func TestSchemaExamplesDecode(t *testing.T) {
	rec := newRecorder()
	a := newAgentWire(t, rec)
	var notes int
	for _, ex := range schemaExamples {
		if ex.method == acp.MethodSessionUpdate {
			a.notify(ex.method, ex.params)
			notes++
			continue
		}
		a.request(ex.method, ex.params)
		if m := a.answer(); m.Error != nil {
			t.Errorf("%s %s: answered %d %s", ex.page, ex.method, m.Error.Code, m.Error.Message)
		}
	}
	calls, _ := rec.snapshot()
	rec.wait(t, notes+len(calls))
	calls, updates := rec.snapshot()
	if len(updates) != notes {
		t.Fatalf("%d of %d session/update examples reached the handler", len(updates), notes)
	}

	kinds := map[acp.UpdateKind]int{}
	for _, u := range updates {
		kinds[u.Update.Kind]++
		known := u.Update.Raw == nil
		switch u.Update.Kind {
		case acp.UpdateUserMessageChunk, acp.UpdateAgentMessageChunk, acp.UpdateAgentThoughtChunk:
			if u.Update.Content == nil || u.Update.Content.Type != "text" || u.Update.Content.Text == "" {
				t.Errorf("chunk without text: %+v", u.Update)
			}
		case acp.UpdateToolCall:
			if u.Update.ToolCall == nil || u.Update.ToolCall.ToolCallID == "" || u.Update.ToolCall.Title == "" {
				t.Errorf("tool_call: %+v", u.Update)
			}
		case acp.UpdateToolCallUpdate:
			if u.Update.ToolCallUpdate == nil || u.Update.ToolCallUpdate.Status == nil {
				t.Errorf("tool_call_update: %+v", u.Update)
			}
		case acp.UpdatePlan:
			if u.Update.Plan == nil || len(u.Update.Plan.Entries) == 0 {
				t.Errorf("plan: %+v", u.Update)
			}
		case acp.UpdateCurrentMode:
			if u.Update.CurrentModeID != "code" {
				t.Errorf("current_mode_update: %+v", u.Update)
			}
		case acp.UpdateUsage:
			want := &acp.UsageUpdate{Used: 53000, Size: 200000, Cost: &acp.Cost{Amount: 0.045, Currency: "USD"}}
			if !reflect.DeepEqual(u.Update.Usage, want) {
				t.Errorf("usage: %+v", u.Update.Usage)
			}
		default:
			known = false
			if u.Update.Raw == nil || !strings.Contains(string(u.Update.Raw), string(u.Update.Kind)) {
				t.Errorf("unknown kind %q without Raw", u.Update.Kind)
			}
		}
		if known && u.Update.Raw != nil {
			t.Errorf("known kind %q kept Raw", u.Update.Kind)
		}
	}
	for _, k := range []acp.UpdateKind{"notice", "compaction_update", "compaction_summary_chunk", "plan_update", "plan_removed", "session_message", "session_message_chunk"} {
		if kinds[k] == 0 {
			t.Errorf("unknown kind %q was not kept", k)
		}
	}

	byMethod := map[string][]any{}
	for _, c := range calls {
		byMethod[c.method] = append(byMethod[c.method], c.req)
	}
	line, limit := uint32(10), uint32(50)
	if got := byMethod[acp.MethodReadTextFile][0]; !reflect.DeepEqual(got, acp.ReadTextFileRequest{SessionID: "sess_abc123def456", Path: "/home/user/project/src/main.py", Line: &line, Limit: &limit}) {
		t.Errorf("read: %+v", got)
	}
	cwd, obl := "/home/user/project", uint64(1048576)
	wantTerm := acp.CreateTerminalRequest{
		SessionID: "sess_abc123def456", Command: "npm", Args: []string{"test", "--coverage"},
		Env: []acp.EnvVariable{{Name: "NODE_ENV", Value: "test"}}, Cwd: &cwd, OutputByteLimit: &obl,
	}
	if got := byMethod[acp.MethodTerminalCreate][0]; !reflect.DeepEqual(got, wantTerm) {
		t.Errorf("terminal/create: %+v", got)
	}
	if got := byMethod[acp.MethodWriteTextFile][0].(acp.WriteTextFileRequest); got.Path != "/home/user/project/config.json" || !strings.Contains(got.Content, `"debug": true`) {
		t.Errorf("write: %+v", got)
	}
	perms := byMethod[acp.MethodRequestPermission]
	if len(perms) != 2 {
		t.Fatalf("%d permission requests", len(perms))
	}
	for _, p := range perms {
		r := p.(acp.RequestPermissionRequest)
		if r.ToolCall.ToolCallID == "" || len(r.Options) == 0 {
			t.Errorf("permission: %+v", r)
		}
		for _, o := range r.Options {
			switch o.Kind {
			case acp.AllowOnce, acp.AllowAlways, acp.RejectOnce, acp.RejectAlways:
			default:
				t.Errorf("option kind %q", o.Kind)
			}
		}
	}
	for _, m := range []string{acp.MethodTerminalOutput, acp.MethodTerminalWaitForExit, acp.MethodTerminalKill, acp.MethodTerminalRelease} {
		if got := byMethod[m]; len(got) != 1 || got[0] != (acp.TerminalRequest{SessionID: "sess_abc123def456", TerminalID: "term_xyz789"}) {
			t.Errorf("%s: %+v", m, got)
		}
	}
}

// decodeOne sends one session/update and returns what reached the handler,
// or false when it was dropped. A valid sentinel update follows it:
// notifications are handled in order, so once the sentinel arrives the
// first one was handled or dropped.
func decodeOne(t *testing.T, update string) (acp.SessionUpdate, bool) {
	t.Helper()
	rec := newRecorder()
	a := newAgentWire(t, rec)
	a.notify(acp.MethodSessionUpdate, `{"sessionId":"s","update":`+update+`}`)
	a.notify(acp.MethodSessionUpdate, `{"sessionId":"sentinel","update":{"sessionUpdate":"session_info_update"}}`)
	for {
		rec.wait(t, 1)
		_, updates := rec.snapshot()
		last := updates[len(updates)-1]
		if last.SessionID != "sentinel" {
			continue
		}
		if len(updates) == 1 {
			return acp.SessionUpdate{}, false
		}
		return updates[0].Update, true
	}
}

func TestUpdateDecodeRules(t *testing.T) {
	big := `"` + strings.Repeat("x", 70<<10) + `"`
	cases := []struct {
		name, update string
		check        func(t *testing.T, u acp.SessionUpdate)
	}{
		{"unknown tool kind is other, bad status is pending", `{"sessionUpdate":"tool_call","toolCallId":"c","title":"T","kind":"teleport","status":"exploded"}`, func(t *testing.T, u acp.SessionUpdate) {
			if u.ToolCall.Kind != acp.ToolOther || u.ToolCall.Status != acp.ToolPending {
				t.Errorf("%+v", u.ToolCall)
			}
		}},
		{"missing kind and status take defaults", `{"sessionUpdate":"tool_call","toolCallId":"c","title":"T","kind":7}`, func(t *testing.T, u acp.SessionUpdate) {
			if u.ToolCall.Kind != acp.ToolOther || u.ToolCall.Status != acp.ToolPending {
				t.Errorf("%+v", u.ToolCall)
			}
		}},
		{"tool content and locations skip invalid items", `{"sessionUpdate":"tool_call","toolCallId":"c","title":"T","kind":"edit",
			"content":[{"type":"diff","path":"/w/a.go","oldText":null,"newText":"new"},{"type":"diff","path":"/w/b.go"},{"type":"session","sessionId":"x"},
			{"type":"terminal","terminalId":"t1"},{"type":"content","content":{"type":"text","text":"ok"}},{"type":"future"},7],
			"locations":[{"path":"/w/a.go","line":3},{"line":4},{"path":"/w/c.go","line":-1}],"rawInput":{"command":"ls"}}`, func(t *testing.T, u acp.SessionUpdate) {
			tc := u.ToolCall
			if len(tc.Content) != 3 || tc.Content[0].Path != "/w/a.go" || tc.Content[0].OldText != nil || tc.Content[0].NewText != "new" ||
				tc.Content[1].TerminalID != "t1" || tc.Content[2].Content.Text != "ok" {
				t.Errorf("content %+v", tc.Content)
			}
			if len(tc.Locations) != 2 || *tc.Locations[0].Line != 3 || tc.Locations[1].Line != nil {
				t.Errorf("locations %+v", tc.Locations)
			}
			if string(tc.RawInput) != `{"command":"ls"}` {
				t.Errorf("rawInput %s", tc.RawInput)
			}
		}},
		{"large rawInput is dropped", `{"sessionUpdate":"tool_call","toolCallId":"c","title":"T","rawInput":` + big + `}`, func(t *testing.T, u acp.SessionUpdate) {
			if u.ToolCall.RawInput != nil {
				t.Error("rawInput kept")
			}
		}},
		{"tool_call_update fields", `{"sessionUpdate":"tool_call_update","toolCallId":"c","kind":"warp","status":"bogus","title":null}`, func(t *testing.T, u acp.SessionUpdate) {
			tu := u.ToolCallUpdate
			if tu.Kind == nil || *tu.Kind != acp.ToolOther || tu.Status != nil || tu.Title != nil {
				t.Errorf("%+v", tu)
			}
		}},
		{"content blocks keep names, not data", `{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"AAAA","mimeType":"image/png"}}`, func(t *testing.T, u acp.SessionUpdate) {
			if *u.Content != (acp.ContentBlock{Type: "image", MimeType: "image/png"}) {
				t.Errorf("%+v", u.Content)
			}
		}},
		{"embedded resource keeps its uri only", `{"sessionUpdate":"user_message_chunk","content":{"type":"resource","resource":{"uri":"file:///w/a.py","text":"secret"}}}`, func(t *testing.T, u acp.SessionUpdate) {
			if *u.Content != (acp.ContentBlock{Type: "resource", URI: "file:///w/a.py"}) {
				t.Errorf("%+v", u.Content)
			}
		}},
		{"resource link", `{"sessionUpdate":"agent_thought_chunk","messageId":"m1","content":{"type":"resource_link","uri":"file:///w/a","name":"a","mimeType":"text/plain","size":3}}`, func(t *testing.T, u acp.SessionUpdate) {
			if *u.Content != (acp.ContentBlock{Type: "resource_link", URI: "file:///w/a", Name: "a", MimeType: "text/plain"}) || u.MessageID != "m1" {
				t.Errorf("%+v", u.Content)
			}
		}},
		{"unknown content type keeps its type", `{"sessionUpdate":"agent_message_chunk","content":{"type":"hologram","x":1}}`, func(t *testing.T, u acp.SessionUpdate) {
			if *u.Content != (acp.ContentBlock{Type: "hologram"}) {
				t.Errorf("%+v", u.Content)
			}
		}},
		{"plan skips invalid entries", `{"sessionUpdate":"plan","entries":[{"content":"a","priority":"high","status":"pending"},{"content":"b","priority":"urgent","status":"pending"},{"content":"c","priority":"low"},{"content":"d","priority":"low","status":"completed"}]}`, func(t *testing.T, u acp.SessionUpdate) {
			if len(u.Plan.Entries) != 2 || u.Plan.Entries[1].Content != "d" {
				t.Errorf("%+v", u.Plan)
			}
		}},
		{"usage with a bad cost keeps the counts", `{"sessionUpdate":"usage_update","used":1,"size":2,"cost":{"amount":"lots"}}`, func(t *testing.T, u acp.SessionUpdate) {
			if u.Usage.Used != 1 || u.Usage.Size != 2 || u.Usage.Cost != nil {
				t.Errorf("%+v", u.Usage)
			}
		}},
		{"session info title", `{"sessionUpdate":"session_info_update","title":"Fix it","updatedAt":"2026-11-27T00:00:00Z"}`, func(t *testing.T, u acp.SessionUpdate) {
			if u.Title != "Fix it" {
				t.Errorf("%+v", u)
			}
		}},
		{"commands and config options keep only the kind", `{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"x"}]}`, func(t *testing.T, u acp.SessionUpdate) {
			if u.Kind != acp.UpdateAvailableCommands || u.Raw != nil {
				t.Errorf("%+v", u)
			}
		}},
		{"unknown kind keeps raw", `{"sessionUpdate":"subagent_update","x":1}`, func(t *testing.T, u acp.SessionUpdate) {
			if u.Kind != "subagent_update" || string(u.Raw) != `{"sessionUpdate":"subagent_update","x":1}` {
				t.Errorf("%+v", u)
			}
		}},
		{"large unknown kind drops raw", `{"sessionUpdate":"future_thing","x":` + big + `}`, func(t *testing.T, u acp.SessionUpdate) {
			if u.Kind != "future_thing" || u.Raw != nil {
				t.Errorf("kind %q raw %d bytes", u.Kind, len(u.Raw))
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, ok := decodeOne(t, c.update)
			if !ok {
				t.Fatal("update was dropped")
			}
			c.check(t, u)
		})
	}
}

func TestInvalidUpdatesAreDropped(t *testing.T) {
	for _, update := range []string{
		`{"sessionUpdate":"tool_call","title":"no id"}`,
		`{"sessionUpdate":"tool_call","toolCallId":"c"}`,
		`{"sessionUpdate":"tool_call_update"}`,
		`{"sessionUpdate":"agent_message_chunk"}`,
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text"}}`,
		`{"sessionUpdate":"plan"}`,
		`{"sessionUpdate":"usage_update","used":-1,"size":2}`,
		`{"sessionUpdate":"current_mode_update"}`,
		`{"kind":"no discriminator"}`,
		`"text"`,
		`null`,
	} {
		if u, ok := decodeOne(t, update); ok {
			t.Errorf("%s reached the handler as %+v", update, u)
		}
	}
}

func TestPermissionRequestSkipsInvalidOptions(t *testing.T) {
	rec := newRecorder()
	a := newAgentWire(t, rec)
	a.request(acp.MethodRequestPermission, `{"sessionId":"s","toolCall":{"toolCallId":"c","kind":"edit","locations":[{"path":"/w/a"}]},
		"options":[{"optionId":"a","name":"A","kind":"allow_once"},{"optionId":"b","name":"B","kind":"allow_forever"},{"name":"C","kind":"reject_once"},{"optionId":"d","name":"D","kind":"reject_once"}]}`)
	if m := a.answer(); m.Error != nil {
		t.Fatal(m.Error)
	}
	calls, _ := rec.snapshot()
	r := calls[0].req.(acp.RequestPermissionRequest)
	if len(r.Options) != 2 || r.Options[0].OptionID != "a" || r.Options[1].OptionID != "d" {
		t.Errorf("options %+v", r.Options)
	}
	if *r.ToolCall.Kind != acp.ToolEdit || r.ToolCall.Locations[0].Path != "/w/a" {
		t.Errorf("tool call %+v", r.ToolCall)
	}
}

func TestReadRequestOptionalFieldsDefault(t *testing.T) {
	rec := newRecorder()
	a := newAgentWire(t, rec)
	a.request(acp.MethodReadTextFile, `{"sessionId":"s","path":"/w/a","line":"ten","limit":-5,"extra":true}`)
	if m := a.answer(); m.Error != nil {
		t.Fatal(m.Error)
	}
	calls, _ := rec.snapshot()
	if got := calls[0].req.(acp.ReadTextFileRequest); got.Line != nil || got.Limit != nil {
		t.Errorf("%+v", got)
	}
}

// The enums of schema/v1/schema.json at schema-v1.35.0 (commit
// 065b57951b6b2a91ac5eddd249e145d1f6d54545) that the contract keeps, and
// the contract constants that must equal them.
func TestContractEnumsMatchTheSchema(t *testing.T) {
	check := func(name string, got []string, want string) {
		t.Helper()
		if g := strings.Join(got, " "); g != want {
			t.Errorf("%s\n got %s\nwant %s", name, g, want)
		}
	}
	check("StopReason", []string{string(acp.StopEndTurn), string(acp.StopMaxTokens), string(acp.StopMaxTurnRequests), string(acp.StopRefusal), string(acp.StopCancelled)},
		"end_turn max_tokens max_turn_requests refusal cancelled")
	check("ToolKind", []string{string(acp.ToolRead), string(acp.ToolEdit), string(acp.ToolDelete), string(acp.ToolMove), string(acp.ToolSearch), string(acp.ToolExecute), string(acp.ToolThink), string(acp.ToolFetch), string(acp.ToolSwitchMode), string(acp.ToolOther)},
		"read edit delete move search execute think fetch switch_mode other")
	check("ToolCallStatus", []string{string(acp.ToolPending), string(acp.ToolInProgress), string(acp.ToolCompleted), string(acp.ToolFailed)},
		"pending in_progress completed failed")
	check("PermissionOptionKind", []string{string(acp.AllowOnce), string(acp.AllowAlways), string(acp.RejectOnce), string(acp.RejectAlways)},
		"allow_once allow_always reject_once reject_always")
	check("RequestPermissionOutcome", []string{acp.OutcomeCancelled, acp.OutcomeSelected}, "cancelled selected")
	// SessionUpdate in the schema also has plan_update, plan_removed,
	// notice, compaction_update, compaction_summary_chunk, subagent_update,
	// session_message and session_message_chunk, which phase 7 keeps as
	// unknown.
	check("UpdateKind", []string{string(acp.UpdateUserMessageChunk), string(acp.UpdateAgentMessageChunk), string(acp.UpdateAgentThoughtChunk), string(acp.UpdateToolCall), string(acp.UpdateToolCallUpdate), string(acp.UpdatePlan), string(acp.UpdateAvailableCommands), string(acp.UpdateCurrentMode), string(acp.UpdateConfigOption), string(acp.UpdateSessionInfo), string(acp.UpdateUsage)},
		"user_message_chunk agent_message_chunk agent_thought_chunk tool_call tool_call_update plan available_commands_update current_mode_update config_option_update session_info_update usage_update")
}

func TestEveryEnumValueDecodes(t *testing.T) {
	for _, k := range []string{"read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other"} {
		for _, st := range []string{"pending", "in_progress", "completed", "failed"} {
			u, ok := decodeOne(t, `{"sessionUpdate":"tool_call","toolCallId":"c","title":"T","kind":"`+k+`","status":"`+st+`"}`)
			if !ok || string(u.ToolCall.Kind) != k || string(u.ToolCall.Status) != st {
				t.Errorf("tool_call %s %s: %+v", k, st, u.ToolCall)
			}
		}
		u, ok := decodeOne(t, `{"sessionUpdate":"tool_call_update","toolCallId":"c","kind":"`+k+`"}`)
		if !ok || u.ToolCallUpdate.Kind == nil || string(*u.ToolCallUpdate.Kind) != k {
			t.Errorf("tool_call_update %s: %+v", k, u.ToolCallUpdate)
		}
	}
	for _, p := range []string{"high", "medium", "low"} {
		for _, st := range []string{"pending", "in_progress", "completed"} {
			u, ok := decodeOne(t, `{"sessionUpdate":"plan","entries":[{"content":"c","priority":"`+p+`","status":"`+st+`"}]}`)
			if !ok || len(u.Plan.Entries) != 1 {
				t.Errorf("plan %s %s: %+v", p, st, u.Plan)
			}
		}
	}
	for _, typ := range []string{
		`{"type":"text","text":"t"}`, `{"type":"image","data":"AA","mimeType":"image/png"}`,
		`{"type":"audio","data":"AA","mimeType":"audio/wav"}`, `{"type":"resource_link","uri":"file:///a","name":"a"}`,
		`{"type":"resource","resource":{"uri":"file:///a","blob":"AA"}}`,
	} {
		if _, ok := decodeOne(t, `{"sessionUpdate":"agent_message_chunk","content":`+typ+`}`); !ok {
			t.Errorf("content %s dropped", typ)
		}
	}
	for _, k := range []string{"available_commands_update", "config_option_update", "session_info_update"} {
		if u, ok := decodeOne(t, `{"sessionUpdate":"`+k+`"}`); !ok || string(u.Kind) != k {
			t.Errorf("%s: %+v", k, u)
		}
	}
	rec := newRecorder()
	a := newAgentWire(t, rec)
	a.request(acp.MethodRequestPermission, `{"sessionId":"s","toolCall":{"toolCallId":"c"},"options":[
		{"optionId":"1","name":"a","kind":"allow_once"},{"optionId":"2","name":"b","kind":"allow_always"},
		{"optionId":"3","name":"c","kind":"reject_once"},{"optionId":"4","name":"d","kind":"reject_always"}]}`)
	if m := a.answer(); m.Error != nil {
		t.Fatal(m.Error)
	}
	calls, _ := rec.snapshot()
	if n := len(calls[0].req.(acp.RequestPermissionRequest).Options); n != 4 {
		t.Errorf("%d of 4 option kinds kept", n)
	}
}

// Every field in the schema's "required" list, removed one at a time.
func TestRequiredFieldsAreRequired(t *testing.T) {
	updates := map[string][]string{
		`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"t"}}`:                    {"content"},
		`{"sessionUpdate":"tool_call","toolCallId":"c","title":"T"}`:                                      {"toolCallId", "title"},
		`{"sessionUpdate":"tool_call_update","toolCallId":"c"}`:                                           {"toolCallId"},
		`{"sessionUpdate":"plan","entries":[]}`:                                                           {"entries"},
		`{"sessionUpdate":"current_mode_update","currentModeId":"code"}`:                                  {"currentModeId"},
		`{"sessionUpdate":"usage_update","used":1,"size":2}`:                                              {"used", "size"},
		`{"sessionUpdate":"agent_thought_chunk","content":{"type":"resource_link","uri":"u","name":"n"}}`: {"content"},
	}
	for full, req := range updates {
		if _, ok := decodeOne(t, full); !ok {
			t.Errorf("%s dropped", full)
		}
		for _, field := range req {
			if _, ok := decodeOne(t, without(t, full, field)); ok {
				t.Errorf("%s without %s was kept", full, field)
			}
		}
	}

	requests := map[string]struct {
		params string
		req    []string
	}{
		acp.MethodReadTextFile:        {`{"sessionId":"s","path":"/a"}`, []string{"sessionId", "path"}},
		acp.MethodWriteTextFile:       {`{"sessionId":"s","path":"/a","content":""}`, []string{"sessionId", "path", "content"}},
		acp.MethodTerminalCreate:      {`{"sessionId":"s","command":"ls"}`, []string{"sessionId", "command"}},
		acp.MethodTerminalOutput:      {`{"sessionId":"s","terminalId":"t"}`, []string{"sessionId", "terminalId"}},
		acp.MethodTerminalWaitForExit: {`{"sessionId":"s","terminalId":"t"}`, []string{"sessionId", "terminalId"}},
		acp.MethodTerminalKill:        {`{"sessionId":"s","terminalId":"t"}`, []string{"sessionId", "terminalId"}},
		acp.MethodTerminalRelease:     {`{"sessionId":"s","terminalId":"t"}`, []string{"sessionId", "terminalId"}},
		acp.MethodRequestPermission:   {`{"sessionId":"s","toolCall":{"toolCallId":"c"},"options":[]}`, []string{"sessionId", "toolCall", "options"}},
	}
	rec := newRecorder()
	a := newAgentWire(t, rec)
	for method, c := range requests {
		a.request(method, c.params)
		if m := a.answer(); m.Error != nil {
			t.Errorf("%s %s: %v", method, c.params, m.Error)
		}
		for _, field := range c.req {
			a.request(method, without(t, c.params, field))
			if code, _ := errorOf(t, a.answer()); code != -32602 {
				t.Errorf("%s without %s: code %d", method, field, code)
			}
		}
	}
}

// without removes one top-level field from a JSON object.
func without(t *testing.T, obj, field string) string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(obj), &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m[field]; !ok {
		t.Fatalf("%s has no %s", obj, field)
	}
	delete(m, field)
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
