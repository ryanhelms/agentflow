package webui_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	_ "github.com/ryanhelms/agentflow/internal/command/ops" // the real ai.respond and ai.cancel Specs
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// consoleRig is a daemon runtime with the session store, the fake agent and
// a Manager hosting detached sessions in this process. The console routes
// answer through the real ai.respond and ai.cancel Specs.
type consoleRig struct {
	home, wt string
	agent    aisessiontest.Installed
	rt       *webui.AIRuntimeForTest
	m        aisession.Manager
	d        *aisessiontest.Detacher
	console  webui.AIConsoleForTest
}

func newConsoleRig(t *testing.T, steps []aisessiontest.Step, set aisessiontest.Settings) *consoleRig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh script")
	}
	agent := aisessiontest.Install(t, "fake-agent", aisessiontest.Script{Steps: steps})
	if set.Overrides == nil {
		set.Overrides = map[string]harnessdef.Definition{}
	}
	set.Overrides["demo"] = harnessdef.Definition{Name: "Demo", Command: "fake-agent"}
	home := aisessiontest.Home(t, set, agent.Dir)
	aisessiontest.Ready(t, home, "demo")
	wt, _ := aisessiontest.Worktree(t)
	st, err := aisession.OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	rt := webui.StartAIRuntimeForTest(t, st, wt)
	detach, d := aisessiontest.InProcessDetach(t)
	m := aisession.NewManager(aisession.Options{
		Home: home, Version: "test", Detach: detach, Emit: rt.Emit,
		WorktreeID: func(context.Context, string) (string, error) { return rt.WorktreeID, nil },
	})
	d.Use(m)
	return &consoleRig{home: home, wt: wt, agent: agent, rt: rt, m: m, d: d, console: webui.NewAIConsoleForTest(wt)}
}

func person(ctx context.Context) context.Context {
	return aisession.WithRequester(ctx, aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceTerminal, Client: "cli"})
}

func (r *consoleRig) start(t *testing.T, prompt string) aisession.Session {
	t.Helper()
	s, err := r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "demo", Prompt: prompt, Dir: r.wt, Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// waitAsk waits until the panel shows one waiting ask for the session.
func (r *consoleRig) waitAsk(t *testing.T) (webui.AISession, webui.AIAsk) {
	t.Helper()
	st := r.rt.WaitState(t, func(st webui.AIState) bool {
		return len(st.Sessions) == 1 && len(st.Sessions[0].Asks) == 1 && st.Sessions[0].State == string(aisession.StateWaiting)
	})
	return st.Sessions[0], st.Sessions[0].Asks[0]
}

func (r *consoleRig) waitEnded(t *testing.T) webui.AISession {
	t.Helper()
	return r.rt.WaitState(t, func(st webui.AIState) bool {
		return len(st.Sessions) == 1 && st.Sessions[0].State == string(aisession.StateEnded)
	}).Sessions[0]
}

func (r *consoleRig) respond(t *testing.T, session, ask, decision, fingerprint string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"session": session, "ask": ask, "decision": decision, "fingerprint": fingerprint})
	if rec := r.console.Post(t, "/api/ai/respond", string(body), nil); rec.Code != 202 || rec.Body.String() != "{}\n" {
		t.Fatalf("respond: %d %s", rec.Code, rec.Body)
	}
}

func writeStep(path, content string) aisessiontest.Step {
	return aisessiontest.Step{Call: &aisessiontest.Call{Method: acp.MethodWriteTextFile, Params: aisessiontest.Params(map[string]any{
		"sessionId": "$SESSION", "path": "$CWD/" + path, "content": content})}}
}

// TestAIPanelFollowsSession: the daemon runtime shows the session, its ask
// with the write preview and its end in the ai projection, and an allow
// from the console route, through the real ai.respond Spec with the
// fingerprint the panel showed, reaches the host. An allow with another
// fingerprint allows nothing.
func TestAIPanelFollowsSession(t *testing.T) {
	content := "hi from the agent\n"
	r := newConsoleRig(t, []aisessiontest.Step{{Update: aisessiontest.Message("working")}, writeStep("src/hello.txt", content)}, aisessiontest.Settings{})
	sess := r.start(t, "say hi")
	if sess.Repo != r.rt.RepoID {
		t.Fatalf("the session's repository %q is not the daemon's %q", sess.Repo, r.rt.RepoID)
	}
	got, ask := r.waitAsk(t)
	raw, _ := json.Marshal(ask.Diff)
	if string(raw) != `{"path":"src/hello.txt","new_text":"hi from the agent\n"}` || len(ask.Paths) != 1 || ask.Paths[0] != ask.Diff.Path {
		t.Errorf("ask diff %s, paths %v", raw, ask.Paths)
	}
	if !ask.CanAllow || ask.Kind != string(aisession.KindWrite) || len(ask.Fingerprint) != 64 || !webui.AIAskRule.MatchString(ask.ID) {
		t.Errorf("ask %+v", ask)
	}
	if got.ID != string(sess.ID) || got.Prompt != "say hi" || got.Harness != "demo" {
		t.Errorf("session %+v", got)
	}
	raw, _ = json.Marshal(got.Counters)
	for _, k := range []string{`"tool_calls"`, `"asks":1`, `"allowed"`, `"denied"`, `"unasked"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("counters %s lack %s", raw, k)
		}
	}

	// An allow for another fingerprint is refused and leaves the ask waiting.
	r.respond(t, got.ID, ask.ID, "allow", strings.Repeat("0", 64))
	time.Sleep(300 * time.Millisecond)
	if _, asks, err := r.m.Get(person(context.Background()), sess.ID); err != nil || len(asks) != 1 {
		t.Fatalf("after a wrong fingerprint: %d asks, %v", len(asks), err)
	}

	// The person allows it from the console with the fingerprint shown.
	r.respond(t, got.ID, ask.ID, "allow", ask.Fingerprint)
	got = r.waitEnded(t)
	if got.Code != aisession.CodeEndTurn || len(got.Asks) != 0 || got.EndedAt == nil {
		t.Errorf("ended session %+v", got)
	}
	r.d.Wait(t, sess.ID, 30*time.Second)
	if b, _ := os.ReadFile(filepath.Join(r.wt, "src", "hello.txt")); string(b) != content {
		t.Errorf("hello.txt = %q", b)
	}
	writes := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodWriteTextFile)
	if len(writes) != 1 || writes[0].ErrorCode != 0 {
		t.Errorf("write = %+v", writes)
	}
	var message string
	for _, it := range got.Items {
		if it.Kind == aisession.ItemMessage {
			message += it.Text
		}
	}
	if message != "working" {
		t.Errorf("items %+v", got.Items)
	}
}

// A console Reject denies the write; nothing is written.
func TestAIConsoleRejects(t *testing.T) {
	r := newConsoleRig(t, []aisessiontest.Step{writeStep("src/no.txt", "no\n")}, aisessiontest.Settings{})
	sess := r.start(t, "write")
	got, ask := r.waitAsk(t)
	r.respond(t, got.ID, ask.ID, "reject", ask.Fingerprint)
	if got = r.waitEnded(t); got.Code != aisession.CodeEndTurn {
		t.Errorf("ended %s", got.Code)
	}
	r.d.Wait(t, sess.ID, 30*time.Second)
	writes := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodWriteTextFile)
	if len(writes) != 1 || writes[0].ErrorCode != acp.CodeDenied || writes[0].ErrorData != aisession.VerdictRejected {
		t.Errorf("write = %+v", writes)
	}
	if _, err := os.Stat(filepath.Join(r.wt, "src", "no.txt")); err == nil {
		t.Error("no.txt was written")
	}
}

// A console Cancel acts at once, even when the person's autonomy policy
// would confirm ai.cancel: the click is the confirmation. Its waiting ask
// is answered cancelled. The same policy still makes agentflow_ai_cancel
// return needs_confirmation.
func TestAIConsoleCancelActsAtOnce(t *testing.T) {
	set := aisessiontest.Settings{Autonomy: config.Autonomy{Local: config.Confirm, Remote: config.Confirm, Actions: map[string]config.Level{"ai.cancel": config.Confirm}}}
	r := newConsoleRig(t, []aisessiontest.Step{writeStep("src/later.txt", "x\n"), {WaitCancel: true}}, set)
	sess := r.start(t, "wait")
	got, _ := r.waitAsk(t)

	// An agent's cancel over MCP asks first and cancels nothing.
	a, err := app.Open(t.Context(), r.wt, "claude")
	if err != nil {
		t.Fatal(err)
	}
	text := callTool(t, a, "agentflow_ai_cancel", map[string]any{"session": got.ID})
	if !strings.Contains(text, "needs_confirmation") {
		t.Fatalf("agent cancel: %s", text)
	}
	if s, _, _ := r.m.Get(person(context.Background()), sess.ID); s.State == aisession.StateEnded || s.State == aisession.StateCancelling {
		t.Fatalf("an unconfirmed agent cancel moved the session to %s", s.State)
	}

	if rec := r.console.Post(t, "/api/ai/cancel", `{"session":"`+got.ID+`"}`, nil); rec.Code != 202 {
		t.Fatalf("cancel: %d %s", rec.Code, rec.Body)
	}
	got = r.waitEnded(t)
	if got.Code != aisession.CodeCancelled {
		t.Errorf("ended %s, want cancelled", got.Code)
	}
	r.d.Wait(t, sess.ID, 30*time.Second)
	writes := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodWriteTextFile)
	if len(writes) != 1 || writes[0].ErrorCode == 0 {
		t.Errorf("the waiting write = %+v, want it answered without a write", writes)
	}
}

// No MCP call can allow: the tool rejects allow, and a person requester on
// its context is replaced by the agent's before the Manager sees it.
func TestAIMCPCannotAllow(t *testing.T) {
	r := newConsoleRig(t, []aisessiontest.Step{writeStep("src/mcp.txt", "x\n")}, aisessiontest.Settings{})
	sess := r.start(t, "write")
	got, ask := r.waitAsk(t)
	a, err := app.Open(t.Context(), r.wt, "claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"session": got.ID, "ask": ask.ID, "decision": "allow"},
		{"session": got.ID, "ask": ask.ID, "decision": "allow", "fingerprint": ask.Fingerprint},
	} {
		if text := callTool(t, a, "agentflow_ai_respond", args); !strings.HasPrefix(text, "error: ") {
			t.Errorf("tool allow %v: %s", args, text)
		}
	}
	c, _ := command.Default.Lookup("ai.respond")
	jc := c.(command.JSONCommand)
	input, _ := json.Marshal(map[string]string{"session": got.ID, "ask": ask.ID, "decision": "allow", "fingerprint": ask.Fingerprint})
	asPerson := aisession.WithRequester(t.Context(), aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceConsole, Client: "repository-console"})
	if res := jc.InvokeJSON(asPerson, command.NewInvoker(command.Options{}), a, input, "", command.Origin{Source: "mcp:claude", Client: "claude"}); res.Err == nil {
		t.Fatalf("an MCP call with a person context allowed: %+v", res.Value)
	}
	if _, asks, _ := r.m.Get(person(context.Background()), sess.ID); len(asks) != 1 {
		t.Fatalf("the ask was answered: %d left", len(asks))
	}
	r.respond(t, got.ID, ask.ID, "reject", ask.Fingerprint)
	r.d.Wait(t, sess.ID, 30*time.Second)
}

// callTool calls one registered tool over an in-memory MCP connection on a,
// with the origin internal/mcpserver gives a call ("mcp:" and the client),
// and returns its text, prefixed "error: " for a tool error.
func callTool(t *testing.T, a *app.App, name string, args map[string]any) string {
	t.Helper()
	c, ok := command.Default.Tool(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	wrap := func(ctx context.Context, _ command.Meta, call func(context.Context, *app.App, command.Origin) (any, error)) *mcp.CallToolResult {
		out, err := call(ctx, a, command.Origin{Source: "mcp:claude", Client: "claude"})
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
		}
		b, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
	}
	c.AddMCP(srv, wrap, command.NewInvoker(command.Options{}))
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(t.Context(), st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return "error: " + err.Error()
	}
	out := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		out = "error: " + out
	}
	return out
}

// With the real Specs registered, the routes refuse malformed bodies and
// fields the page never sends, answer 202 with no session data, and allow
// 30 requests a minute per console across both routes.
func TestAIRoutesWithTheRealCommands(t *testing.T) {
	r := newConsoleRig(t, nil, aisessiontest.Settings{})
	sess := string(aisession.NewID())
	ask, fp := "ask_0123456789abcdef", strings.Repeat("ab", 32)
	good := `{"session":"` + sess + `","ask":"` + ask + `","decision":"reject","fingerprint":"` + fp + `"}`
	now := time.Now()
	webui.SetAINowForTest(t, &now)
	for _, body := range []string{
		`{"session":"x","ask":"` + ask + `","decision":"allow","fingerprint":"` + fp + `"}`,
		`{"session":"` + sess + `","ask":"ask_1","decision":"allow","fingerprint":"` + fp + `"}`,
		`{"session":"` + sess + `","ask":"` + ask + `","decision":"allow_always","fingerprint":"` + fp + `"}`,
		`{"session":"` + sess + `","ask":"` + ask + `","decision":"allow","fingerprint":"short"}`,
		`{"session":"` + sess + `","ask":"` + ask + `","decision":"allow","fingerprint":"` + fp + `","by":"agent"}`,
	} {
		if w := r.console.Post(t, "/api/ai/respond", body, nil); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if w := r.console.Post(t, "/api/ai/cancel", `{"session":"`+sess+`","by":"agent"}`, nil); w.Code != 400 {
		t.Errorf("cancel with an extra field: %d", w.Code)
	}
	for i := range 30 {
		path, body := "/api/ai/respond", good
		if i%2 == 1 {
			path, body = "/api/ai/cancel", `{"session":"`+sess+`"}`
		}
		if w := r.console.Post(t, path, body, nil); w.Code != 202 || w.Body.String() != "{}\n" {
			t.Fatalf("request %d: %d %s", i, w.Code, w.Body)
		}
	}
	if w := r.console.Post(t, "/api/ai/respond", good, nil); w.Code != 429 {
		t.Errorf("past the limit: %d", w.Code)
	}
	now = now.Add(time.Minute)
	if w := r.console.Post(t, "/api/ai/respond", good, nil); w.Code != 202 {
		t.Errorf("a minute later: %d", w.Code)
	}
}
