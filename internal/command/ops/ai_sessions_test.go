package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
	"github.com/ryanhelms/agentflow/internal/command/ops"
)

const (
	testSession = aisession.ID("ais_0123456789abcdef0123456789abcdef")
	testAsk     = "ask_0123456789abcdef"
)

// fakeManager stands in for aisession.Manager: it records what each call
// was asked and by whom, and starts nothing.
type fakeManager struct {
	mu         sync.Mutex
	requesters []aisession.Requester
	starts     []aisession.StartRequest
	answers    []aisession.AnswerRequest
	cancels    int
	refuse     error
	options    aisession.Options
}

var fake = &fakeManager{}

func init() {
	app.NewAIManager = func(o aisession.Options) aisession.Manager {
		fake.mu.Lock()
		fake.options = o
		fake.mu.Unlock()
		return fake
	}
}

func resetFake(t *testing.T) *fakeManager {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.requesters, fake.starts, fake.answers, fake.cancels, fake.refuse = nil, nil, nil, 0, nil
	// A test binary has no terminal and may run inside an agent: name the
	// requester explicitly where a test needs a person.
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_CODEX_HOST_ACTIVE", app.SessionEnv} {
		t.Setenv(name, "")
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CODEX_") && name != "CODEX_HOME" {
			t.Setenv(name, "")
			_ = os.Unsetenv(name)
		}
	}
	return fake
}

func (f *fakeManager) record(ctx context.Context) {
	r, ok := aisession.RequesterFrom(ctx)
	if !ok {
		r = aisession.Requester{Kind: "none"}
	}
	f.mu.Lock()
	f.requesters = append(f.requesters, r)
	f.mu.Unlock()
}

func (f *fakeManager) last() aisession.Requester {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requesters) == 0 {
		return aisession.Requester{}
	}
	return f.requesters[len(f.requesters)-1]
}

func (f *fakeManager) Prepare(ctx context.Context, r aisession.StartRequest) (aisession.Plan, error) {
	f.record(ctx)
	if f.refuse != nil {
		return aisession.Plan{}, f.refuse
	}
	by, _ := aisession.RequesterFrom(ctx)
	return aisession.Plan{Harness: r.Harness, HarnessName: "Fake", Worktree: r.Dir, StartedBy: by, Prompt: r.Prompt, Detached: r.Detach,
		EnvNames: []string{"HOME", "PATH"}, Limits: aisession.Limits{MaxDuration: aisession.DefaultMaxDuration}}, nil
}

func (f *fakeManager) Start(ctx context.Context, r aisession.StartRequest) (aisession.Session, error) {
	if _, err := f.Prepare(ctx, r); err != nil {
		return aisession.Session{}, err
	}
	f.mu.Lock()
	f.starts = append(f.starts, r)
	f.mu.Unlock()
	mode := aisession.HostForeground
	if r.Detach {
		mode = aisession.HostDetached
	}
	return aisession.Session{ID: testSession, Harness: r.Harness, Worktree: r.Dir, State: aisession.StateStarting, Host: aisession.HostInfo{Mode: mode}}, nil
}

func (f *fakeManager) Host(context.Context, aisession.ID, aisession.HostOptions) (aisession.Session, error) {
	return aisession.Session{}, errors.New("the fake hosts nothing")
}

func (f *fakeManager) List(ctx context.Context) ([]aisession.Session, error) {
	f.record(ctx)
	return []aisession.Session{{ID: testSession, Harness: "fake", State: aisession.StateWaiting, CreatedAt: time.Now()}}, nil
}

func (f *fakeManager) Get(ctx context.Context, id aisession.ID) (aisession.Session, []aisession.AskRecord, error) {
	f.record(ctx)
	if id != testSession {
		return aisession.Session{}, nil, &aisession.Refusal{Code: aisession.AnswerNotFound, Message: "no such session"}
	}
	return aisession.Session{ID: id, Harness: "fake", State: aisession.StateWaiting},
		[]aisession.AskRecord{{ID: testAsk, Session: id, Kind: aisession.KindWrite, Paths: []string{"src/hello.txt"}, Reason: aisession.ReasonCeiling, CanAllow: true, Fingerprint: "f00d"}}, nil
}

func (f *fakeManager) Items(ctx context.Context, id aisession.ID, since int64, max int) ([]aisession.Item, int64, error) {
	f.record(ctx)
	return []aisession.Item{{Seq: since + 1, Kind: aisession.ItemMessage, Text: "hello\x1b[31m"}}, since + 1, nil
}

func (f *fakeManager) Answer(ctx context.Context, r aisession.AnswerRequest) (aisession.AnswerResult, error) {
	f.record(ctx)
	by, _ := aisession.RequesterFrom(ctx)
	if r.Decision == aisession.Allow && by.Kind != aisession.RequesterPerson {
		return aisession.AnswerResult{}, &aisession.Refusal{Code: aisession.AnswerNeedsPerson, Message: "only a person can allow"}
	}
	f.mu.Lock()
	f.answers = append(f.answers, r)
	f.mu.Unlock()
	return aisession.AnswerResult{Session: r.Session, Ask: r.Ask, Decision: r.Decision, State: aisession.StateRunning}, nil
}

func (f *fakeManager) Cancel(ctx context.Context, id aisession.ID) (aisession.Session, error) {
	f.record(ctx)
	f.mu.Lock()
	f.cancels++
	f.mu.Unlock()
	return aisession.Session{ID: id, State: aisession.StateCancelling}, nil
}

func (f *fakeManager) Prune(context.Context) error { return nil }

// aiTool serves one registered tool over an in-memory MCP connection whose
// calls run on a, as the MCP server runs them.
func aiTool(t *testing.T, a *app.App, name string) (*mcp.ClientSession, *mcp.Tool) {
	t.Helper()
	c, ok := command.Default.Tool(name)
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	_, inv := recording()
	return connectTool(t, c, a, inv)
}

func callText(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error(), true
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

// No tool input names a directory, a repository, a requester, an
// environment, a limit or a ceiling: the properties are exactly the ADR's.
func TestAIToolInputsNameNoPath(t *testing.T) {
	f := newFixture(t)
	for tool, want := range map[string][]string{
		"agentflow_ai_run":     {"confirm_token", "harness", "prompt"},
		"agentflow_ai_status":  {"session", "since"},
		"agentflow_ai_respond": {"ask", "decision", "session"},
		"agentflow_ai_cancel":  {"confirm_token", "session"},
	} {
		_, spec := aiTool(t, f.app, tool)
		raw, _ := json.Marshal(spec.InputSchema)
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		var got []string
		for k := range schema.Properties {
			got = append(got, k)
		}
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s inputs %v, want %v", tool, got, want)
		}
		if tool == "agentflow_ai_respond" && !strings.Contains(string(schema.Properties["decision"]), `"enum":["reject"]`) {
			t.Errorf("respond decision schema %s, want enum [reject]", schema.Properties["decision"])
		}
	}
}

// ai run always asks first, even with autonomy auto (once lane 7.2 adds the
// ai.run action, actions.ai.run: auto belongs here too), and an MCP call is an
// agent's: detached, in the server's own worktree.
func TestAIRunAlwaysNeedsConfirmation(t *testing.T) {
	f := newFixture(t)
	m := resetFake(t)
	if err := os.WriteFile(filepath.Join(f.app.Home, "config.yml"), []byte("autonomy: {local: auto, remote: auto}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := app.Open(t.Context(), f.dir, "claude")
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := aiTool(t, a, "agentflow_ai_run")
	text, isErr := callText(t, cs, "agentflow_ai_run", map[string]any{"harness": "fake", "prompt": "say hi"})
	if isErr || !strings.Contains(text, "needs_confirmation") || !strings.Contains(text, "confirm_token") {
		t.Fatalf("first call: %s", text)
	}
	if len(m.starts) != 0 {
		t.Fatal("a start ran before the confirmation")
	}
	if r := m.last(); r.Kind != aisession.RequesterAgent || r.Surface != aisession.SurfaceMCP || r.Client != "claude" {
		t.Fatalf("requester %+v, want an MCP agent", r)
	}
	var pending struct {
		Token   string         `json:"confirm_token"`
		Summary aisession.Plan `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text), &pending); err != nil {
		t.Fatal(err)
	}
	if !pending.Summary.Detached || pending.Summary.Worktree != a.Repo.Dir {
		t.Fatalf("summary %+v: want detached in the server's worktree", pending.Summary)
	}
	text, isErr = callText(t, cs, "agentflow_ai_run", map[string]any{"harness": "fake", "prompt": "say hi", "confirm_token": pending.Token})
	if isErr || !strings.Contains(text, string(testSession)) {
		t.Fatalf("confirmed call: %s", text)
	}
	if len(m.starts) != 1 || !m.starts[0].Detach || m.starts[0].Dir != a.Repo.Dir {
		t.Fatalf("starts %+v", m.starts)
	}
}

// The Manager's refusals reach the caller with their code, and nothing
// starts.
func TestAIRunRefusalsReachTheCaller(t *testing.T) {
	f := newFixture(t)
	m := resetFake(t)
	for _, code := range []string{aisession.RefuseAgentStartOff, aisession.RefuseRepoDisabled, aisession.RefusePrimaryCheckout, aisession.RefuseNotReady, aisession.RefusePluginLaunch, aisession.RefuseLimitRunning} {
		m.refuse = &aisession.Refusal{Code: code, Message: "the session was refused", Fix: "Run agentflow start <task> --worktree."}
		cs, _ := aiTool(t, f.app, "agentflow_ai_run")
		text, isErr := callText(t, cs, "agentflow_ai_run", map[string]any{"harness": "fake", "prompt": "x"})
		if !isErr || !strings.Contains(text, code) || !strings.Contains(text, "agentflow start <task> --worktree") {
			t.Errorf("tool, %s: %s", code, text)
		}
		env := &commandtest.Env{App: f.app, Answer: true}
		_, inv := recording()
		err := dispatch(t, env, inv, "ai", "run", "--harness", "fake", "--yes", "x")
		var r *aisession.Refusal
		if err == nil || !errors.As(err, &r) || r.Code != code || !strings.Contains(err.Error(), code) || !strings.Contains(err.Error(), "--worktree") {
			t.Errorf("CLI, %s: %v", code, err)
		}
	}
	if len(m.starts) != 0 {
		t.Fatalf("started %+v", m.starts)
	}
}

// A session never starts, answers or cancels: the session's own client and
// the session variable are refused nested before the Manager is asked.
func TestSessionsCannotStartSessions(t *testing.T) {
	f := newFixture(t)
	m := resetFake(t)
	app.SessionLookup = func(context.Context, string, string) (string, bool, error) { return f.dir, false, nil }
	t.Cleanup(func() { app.SessionLookup = nil })
	session, err := app.OpenSession(t.Context(), f.dir, string(testSession))
	if err != nil {
		t.Fatal(err)
	}
	cs, _ := aiTool(t, session, "agentflow_ai_run")
	if text, isErr := callText(t, cs, "agentflow_ai_run", map[string]any{"harness": "fake", "prompt": "x"}); !isErr || !strings.Contains(text, "cannot start") {
		t.Fatalf("session client: %s", text)
	}
	cs, _ = aiTool(t, session, "agentflow_ai_respond")
	if text, isErr := callText(t, cs, "agentflow_ai_respond", map[string]any{"session": string(testSession), "ask": testAsk, "decision": "reject"}); !isErr || !strings.Contains(text, "cannot answer") {
		t.Fatalf("session answer: %s", text)
	}
	t.Setenv(app.SessionEnv, string(testSession))
	env := &commandtest.Env{App: f.app, Answer: true}
	_, inv := recording()
	if err := dispatch(t, env, inv, "ai", "run", "--harness", "fake", "--yes", "x"); err == nil || !strings.Contains(err.Error(), "cannot start") {
		t.Fatalf("CLI inside a session: %v", err)
	}
	if len(m.starts) != 0 || len(m.answers) != 0 {
		t.Fatalf("starts %v answers %v", m.starts, m.answers)
	}
}

// An agent can reject but never allow: the tool refuses allow, and the CLI
// --allow refuses without a person at a terminal, whatever --yes says.
func TestAIRespondOnlyPersonsAllow(t *testing.T) {
	f := newFixture(t)
	m := resetFake(t)
	cs, _ := aiTool(t, f.app, "agentflow_ai_respond")
	if text, isErr := callText(t, cs, "agentflow_ai_respond", map[string]any{"session": string(testSession), "ask": testAsk, "decision": "allow"}); !isErr {
		t.Fatalf("an agent allowed: %s", text)
	}
	if text, isErr := callText(t, cs, "agentflow_ai_respond", map[string]any{"session": string(testSession), "ask": testAsk, "decision": "reject"}); isErr || !strings.Contains(text, `"deny"`) {
		t.Fatalf("reject: %s", text)
	}
	if r := m.last(); r.Kind != aisession.RequesterAgent {
		t.Fatalf("requester %+v", r)
	}

	env := &commandtest.Env{App: f.app, Answer: true}
	_, inv := recording()
	ops.StdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { ops.StdinIsTerminal = func() bool { return false } })
	err := dispatch(t, env, inv, "ai", "respond", string(testSession), testAsk, "--allow", "--yes")
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("--allow without a terminal: %v", err)
	}
	t.Setenv("CLAUDECODE", "1")
	ops.StdinIsTerminal = func() bool { return true }
	if err := dispatch(t, env, inv, "ai", "respond", string(testSession), testAsk, "--allow"); err == nil || !strings.Contains(err.Error(), "AI agent") {
		t.Fatalf("--allow in an agent: %v", err)
	}
	for _, a := range m.answers {
		if a.Decision == aisession.Allow {
			t.Fatalf("an allow was written: %+v", m.answers)
		}
	}
	if err := dispatch(t, env, inv, "ai", "respond", string(testSession), testAsk, "--reject"); err != nil {
		t.Fatal(err)
	}
	if r := m.last(); r.Kind != aisession.RequesterAgent || r.Surface != aisession.SurfaceCLI {
		t.Fatalf("CLI in an agent: requester %+v", r)
	}
}

// The command line is a person's only with a terminal and no agent.
func TestCLIRequester(t *testing.T) {
	f := newFixture(t)
	m := resetFake(t)
	env := &commandtest.Env{App: f.app, Answer: true}
	_, inv := recording()
	t.Cleanup(func() { ops.StdinIsTerminal = func() bool { return false } })
	for _, sc := range []struct {
		terminal bool
		agent    string
		want     string
	}{
		{false, "", aisession.RequesterAgent},
		{true, "1", aisession.RequesterAgent},
		{true, "", aisession.RequesterPerson},
	} {
		ops.StdinIsTerminal = func() bool { return sc.terminal }
		t.Setenv("CLAUDECODE", sc.agent)
		if err := dispatch(t, env, inv, "ai", "status", "--json"); err != nil {
			t.Fatal(err)
		}
		if r := m.last(); r.Kind != sc.want {
			t.Errorf("terminal %v agent %q: requester %+v, want %s", sc.terminal, sc.agent, r, sc.want)
		}
	}
}

// ai status lists sessions with their waiting asks, and one session's items;
// text output neutralizes terminal sequences.
func TestAIStatus(t *testing.T) {
	f := newFixture(t)
	resetFake(t)
	cs, _ := aiTool(t, f.app, "agentflow_ai_status")
	text, isErr := callText(t, cs, "agentflow_ai_status", map[string]any{})
	if isErr || !strings.Contains(text, testAsk) || !strings.Contains(text, `"sessions"`) {
		t.Fatalf("list: %s", text)
	}
	text, isErr = callText(t, cs, "agentflow_ai_status", map[string]any{"session": string(testSession), "since": 4})
	if isErr || !strings.Contains(text, `"next_seq":5`) {
		t.Fatalf("items: %s", text)
	}
	if text, isErr = callText(t, cs, "agentflow_ai_status", map[string]any{"session": "../x"}); !isErr {
		t.Fatalf("a bad ID was accepted: %s", text)
	}
}

// ai cancel follows the confirmation flow and publishes worktree events.
func TestAICancel(t *testing.T) {
	f := newFixture(t)
	m := resetFake(t)
	em, inv := recording()
	env := &commandtest.Env{App: f.app, Answer: true}
	if err := dispatch(t, env, inv, "ai", "cancel", string(testSession), "--json"); err != nil {
		t.Fatal(err)
	}
	if m.cancels != 1 || !strings.Contains(env.Out.String(), "cancelling") {
		t.Fatalf("cancels %d out %s", m.cancels, env.Out.String())
	}
	types := em.Types()
	if len(types) == 0 || types[0] != bus.TypeOpRequested || types[len(types)-1] != bus.TypeOpApplied {
		t.Fatalf("events %v", types)
	}
}

// The production Options give the harness this binary's read-only MCP
// server with no env values, and nothing when the binary is inside the
// worktree.
func TestAISessionOptionsMCPServer(t *testing.T) {
	f := newFixture(t)
	o := ops.AISessionOptions(f.app)
	srv := o.MCPServer(testSession)
	if srv == nil {
		t.Fatal("no MCP server")
	}
	if srv.Name != "agentflow" || !filepath.IsAbs(srv.Command) || !slices.Equal(srv.Args, []string{"mcp", "--session", string(testSession)}) || srv.Env == nil || len(srv.Env) != 0 {
		t.Fatalf("server %+v", srv)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if dir := filepath.Dir(exe); dir != "" {
		repo := *f.app.Repo
		repo.Dir = dir
		a := &app.App{Home: f.app.Home, Repo: &repo, Client: "cli"}
		if s := ops.AISessionOptions(a).MCPServer(testSession); s != nil {
			t.Fatalf("a binary inside the worktree was passed: %+v", s)
		}
	}
	if o.Home != f.app.Home || o.Version == "" || o.WorktreeID == nil || o.PluginDeny == nil || o.MergeBase == nil || o.Emit == nil || o.WorkDirs == nil {
		t.Fatalf("options %+v", o)
	}
	// A test binary reads no console registry (the account's own).
	if dirs, err := o.WorkDirs(t.Context()); err != nil || len(dirs) != 0 {
		t.Fatalf("work dirs in a test binary: %v, %v", dirs, err)
	}
}

// The console allows through ai.respond with the person requester it sets
// and the fingerprint it showed. No other requester can allow, and an MCP
// call is an agent's whatever its context says.
func TestAIRespondAllowFromTheConsoleOnly(t *testing.T) {
	f := newFixture(t)
	m := resetFake(t)
	c, ok := command.Default.Lookup("ai.respond")
	if !ok {
		t.Fatal("ai.respond is not registered")
	}
	jc := c.(command.JSONCommand)
	_, inv := recording()
	input := json.RawMessage(`{"session":"` + string(testSession) + `","ask":"` + testAsk + `","decision":"allow","fingerprint":"f00d"}`)
	console := command.Origin{Source: "daemon", Client: "repository-console"}

	if res := jc.InvokeJSON(t.Context(), inv, f.app, input, "", console); res.Err == nil {
		t.Fatalf("allow without a requester: %+v", res.Value)
	}
	agent := aisession.WithRequester(t.Context(), aisession.Requester{Kind: aisession.RequesterAgent, Surface: aisession.SurfaceMCP})
	if res := jc.InvokeJSON(agent, inv, f.app, input, "", console); res.Err == nil {
		t.Fatalf("allow by an agent: %+v", res.Value)
	}
	// A person requester on the context of an MCP call is replaced by the
	// agent's.
	person := aisession.WithRequester(t.Context(), aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceConsole, Client: "repository-console"})
	if res := jc.InvokeJSON(person, inv, f.app, input, "", command.Origin{Source: "mcp:claude", Client: "claude"}); res.Err == nil {
		t.Fatalf("allow over MCP with a person context: %+v", res.Value)
	}
	if len(m.answers) != 0 {
		t.Fatalf("answers written: %+v", m.answers)
	}
	res := jc.InvokeJSON(person, inv, f.app, input, "", console)
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	if len(m.answers) != 1 || m.answers[0].Decision != aisession.Allow || m.answers[0].Fingerprint != "f00d" {
		t.Fatalf("answers %+v", m.answers)
	}
	if r := m.last(); r.Kind != aisession.RequesterPerson || r.Surface != aisession.SurfaceConsole {
		t.Fatalf("requester %+v", r)
	}

	// The tool refuses allow, with or without a fingerprint, and its schema
	// has no fingerprint.
	cs, _ := aiTool(t, f.app, "agentflow_ai_respond")
	for _, args := range []map[string]any{
		{"session": string(testSession), "ask": testAsk, "decision": "allow"},
		{"session": string(testSession), "ask": testAsk, "decision": "allow", "fingerprint": "f00d"},
	} {
		if text, isErr := callText(t, cs, "agentflow_ai_respond", args); !isErr {
			t.Errorf("tool allowed with %v: %s", args, text)
		}
	}
	if len(m.answers) != 1 {
		t.Fatalf("a tool call wrote an answer: %+v", m.answers)
	}
}
