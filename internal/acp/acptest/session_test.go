package acptest_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/jsonrpc"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

// TestMain lets acptest.Install start this test binary as the fake agent.
func TestMain(m *testing.M) {
	jsonrpctest.RunChild()
	os.Exit(m.Run())
}

// host is a ClientHandler standing in for lane 7.2's session host. It
// never runs a program: terminals are fake IDs. Reads and writes outside
// the worktree are denied as the chain would; a write to .claude/ is
// denied control_file; permission requests are rejected, or held until
// cancelled when hold is set.
type host struct {
	worktree string
	hold     bool
	asked    chan struct{}
	release  chan struct{} // closed when the session is cancelled

	mu      sync.Mutex
	updates []acp.SessionNotification
	methods []string
}

func newHost(worktree string) *host {
	return &host{worktree: worktree, asked: make(chan struct{}, 16), release: make(chan struct{})}
}

func (h *host) saw(m string) {
	h.mu.Lock()
	h.methods = append(h.methods, m)
	h.mu.Unlock()
}

func deny(code string) error {
	return &jsonrpc.Error{Code: acp.CodeDenied, Message: "denied", Data: json.RawMessage(`{"code":"` + code + `"}`)}
}

func (h *host) inside(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", deny("relative_path")
	}
	rel, err := filepath.Rel(h.worktree, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", deny("outside_worktree")
	}
	return rel, nil
}

func (h *host) SessionUpdate(_ context.Context, n acp.SessionNotification) {
	h.mu.Lock()
	h.updates = append(h.updates, n)
	h.mu.Unlock()
}

func (h *host) RequestPermission(ctx context.Context, _ acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	h.saw(acp.MethodRequestPermission)
	h.asked <- struct{}{}
	if h.hold {
		select {
		case <-ctx.Done():
		case <-h.release:
		}
		return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeCancelled}}, nil
	}
	return acp.RequestPermissionResponse{Outcome: acp.PermissionOutcome{Outcome: acp.OutcomeSelected, OptionID: "reject-once"}}, nil
}

func (h *host) ReadTextFile(_ context.Context, r acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	h.saw(acp.MethodReadTextFile)
	rel, err := h.inside(r.Path)
	if err != nil {
		return acp.ReadTextFileResponse{}, err
	}
	b, err := os.ReadFile(filepath.Join(h.worktree, rel))
	if err != nil {
		return acp.ReadTextFileResponse{}, &jsonrpc.Error{Code: acp.CodeResourceNotFound, Message: "not found"}
	}
	return acp.ReadTextFileResponse{Content: string(b)}, nil
}

func (h *host) WriteTextFile(_ context.Context, r acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	h.saw(acp.MethodWriteTextFile)
	rel, err := h.inside(r.Path)
	if err != nil {
		return acp.WriteTextFileResponse{}, err
	}
	if strings.HasPrefix(rel, ".claude") {
		return acp.WriteTextFileResponse{}, deny("control_file")
	}
	return acp.WriteTextFileResponse{}, deny("timeout") // nobody answered the ask
}

func (h *host) CreateTerminal(_ context.Context, r acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	h.saw(acp.MethodTerminalCreate + " " + r.Command + " " + strings.Join(r.Args, " "))
	if r.Command == "agentflow" {
		return acp.CreateTerminalResponse{}, deny("agentflow_in_session")
	}
	for _, a := range r.Args {
		if strings.Contains(a, "$AGENTFLOW_HOME") {
			return acp.CreateTerminalResponse{}, deny("agentflow_home")
		}
	}
	return acp.CreateTerminalResponse{TerminalID: "term-1"}, nil // never started
}

func (h *host) TerminalOutput(context.Context, acp.TerminalRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}

func (h *host) WaitForTerminalExit(_ context.Context, r acp.TerminalRequest) (acp.TerminalExitStatus, error) {
	h.saw(acp.MethodTerminalWaitForExit + " " + r.TerminalID)
	zero := uint32(0)
	return acp.TerminalExitStatus{ExitCode: &zero}, nil
}

func (h *host) KillTerminal(context.Context, acp.TerminalRequest) error { return nil }

func (h *host) ReleaseTerminal(_ context.Context, r acp.TerminalRequest) error {
	h.saw(acp.MethodTerminalRelease + " " + r.TerminalID)
	return nil
}

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// outsideCode is what the test host answers for the demo's /etc/hosts
// read: on Windows that path is not absolute.
func outsideCode() string {
	if runtime.GOOS == "windows" {
		return "relative_path"
	}
	return "outside_worktree"
}

// session is one fake agent launched through jsonrpc.Launch.
type session struct {
	proc     jsonrpc.Process
	log      string
	worktree string
	host     *host
}

func launch(t *testing.T, s *acptest.SessionScript, configure func(*host)) *session {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	data := filepath.Join(root, "data")
	worktree := filepath.Join(root, "wt")
	for _, d := range []string{bin, data, worktree} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, "notes.txt"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "fake.log")
	args := acptest.Install(t, bin, "fake-acp", acptest.Script{Log: logPath, Session: s})
	env, err := jsonrpc.HarnessEnv(os.Environ(), data, []string{bin})
	if err != nil {
		t.Fatal(err)
	}
	env = append(env, "AGENTFLOW_ACP_SESSION=af-session-7")
	h := newHost(worktree)
	if configure != nil {
		configure(h)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	proc, err := jsonrpc.Launch(ctx, jsonrpc.LaunchSpec{
		Path:     filepath.Join(bin, exe("fake-acp")),
		Root:     bin,
		Dir:      data,
		DataDir:  data,
		Args:     args,
		Env:      env,
		AllowEnv: []string{"AGENTFLOW_ACP_SESSION"},
		Limits:   acp.SessionLimits,
		Handler:  acp.NewClientHandler(h),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Kill() })
	return &session{proc: proc, log: logPath, worktree: worktree, host: h}
}

func (s *session) start(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	init, err := acp.Initialize(ctx, s.proc.Conn(), acp.SessionInitialize("1.0.0"))
	if err != nil || init.ProtocolVersion != acp.ProtocolVersion {
		t.Fatalf("initialize: %+v %v", init, err)
	}
	ns, err := acp.NewSession(ctx, s.proc.Conn(), acp.NewSessionRequest{
		Cwd:        s.worktree,
		MCPServers: []acp.MCPServer{{Name: "agentflow", Command: "/nonexistent/agentflow", Args: []string{"mcp", "--session", "af-session-7"}}},
	})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	return ns.SessionID
}

func (s *session) prompt(t *testing.T, id string) (acp.PromptResponse, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return acp.Prompt(ctx, s.proc.Conn(), acp.PromptRequest{SessionID: id, Prompt: []acp.ContentBlock{{Type: "text", Text: "Add a greeting"}}})
}

func (s *session) asks(t *testing.T) []acptest.LogEntry {
	t.Helper()
	entries, err := acptest.ReadLog(s.log)
	if err != nil {
		t.Fatal(err)
	}
	var out []acptest.LogEntry
	for _, e := range entries {
		if e.Asked != "" {
			out = append(out, e)
		}
	}
	return out
}

func TestSessionDemoScript(t *testing.T) {
	s := launch(t, acptest.SessionDemo(), nil)
	id := s.start(t)
	if id != acptest.DefaultSessionID {
		t.Fatalf("session ID %q", id)
	}
	resp, err := s.prompt(t, id)
	if err != nil || resp.StopReason != acp.StopEndTurn {
		t.Fatalf("prompt: %+v %v", resp, err)
	}

	type want struct {
		method, errData, answer string
	}
	wants := []want{
		{acp.MethodReadTextFile, "", `{"content":"hello\n"}`},
		{acp.MethodRequestPermission, "", `{"outcome":{"outcome":"selected","optionId":"reject-once"}}`},
		{acp.MethodWriteTextFile, "timeout", ""},
		{acp.MethodReadTextFile, outsideCode(), ""},
		{acp.MethodWriteTextFile, "control_file", ""},
		{acp.MethodTerminalCreate, "", `{"terminalId":"term-1"}`},
		{acp.MethodTerminalWaitForExit, "", `{"exitCode":0,"signal":null}`},
		{acp.MethodTerminalRelease, "", `{}`},
	}
	asks := s.asks(t)
	if len(asks) != len(wants) {
		t.Fatalf("asks %+v", asks)
	}
	for i, w := range wants {
		a := asks[i]
		if a.Asked != w.method || a.ErrData != w.errData || string(a.Answer) != w.answer {
			t.Errorf("ask %d: %+v, want %+v", i, a, w)
		}
		if w.errData != "" && a.Code != acp.CodeDenied {
			t.Errorf("ask %d: code %d", i, a.Code)
		}
	}

	s.host.mu.Lock()
	updates := append([]acp.SessionNotification(nil), s.host.updates...)
	s.host.mu.Unlock()
	var kinds []string
	for _, u := range updates {
		if u.SessionID != id {
			t.Errorf("update for %q", u.SessionID)
		}
		k := string(u.Update.Kind)
		if u.Update.ToolCallUpdate != nil && u.Update.ToolCallUpdate.Status != nil {
			k += ":" + string(*u.Update.ToolCallUpdate.Status)
		}
		if u.Update.ToolCall != nil {
			k += ":" + string(u.Update.ToolCall.Kind) + ":" + string(u.Update.ToolCall.Status)
		}
		kinds = append(kinds, k)
	}
	wantKinds := "agent_message_chunk agent_message_chunk plan tool_call:edit:pending tool_call_update:failed tool_call:execute:in_progress tool_call_update:completed usage_update agent_message_chunk"
	if got := strings.Join(kinds, " "); got != wantKinds {
		t.Errorf("updates\n got %s\nwant %s", got, wantKinds)
	}
	if u := updates[7].Update.Usage; u == nil || u.Cost == nil || u.Cost.Currency != "USD" {
		t.Errorf("usage %+v", u)
	}

	// The fake logs the mcpServers it was given and never starts them.
	entries, _ := acptest.ReadLog(s.log)
	var sawNew bool
	for _, e := range entries {
		if e.Method == acp.MethodSessionNew {
			sawNew = strings.Contains(string(e.Params), `"/nonexistent/agentflow"`) && strings.Contains(string(e.Params), `"cwd":`)
		}
	}
	if !sawNew {
		t.Error("session/new params with mcpServers not logged")
	}
}

func TestSessionNestedScript(t *testing.T) {
	s := launch(t, acptest.SessionNested(), nil)
	resp, err := s.prompt(t, s.start(t))
	if err != nil || resp.StopReason != acp.StopEndTurn {
		t.Fatalf("prompt: %+v %v", resp, err)
	}
	asks := s.asks(t)
	want := []string{"agentflow_in_session", "agentflow_in_session", "agentflow_home"}
	if len(asks) != len(want) {
		t.Fatalf("asks %+v", asks)
	}
	for i, w := range want {
		if asks[i].Asked != acp.MethodTerminalCreate || asks[i].ErrData != w {
			t.Errorf("ask %d: %+v, want %s", i, asks[i], w)
		}
	}
	s.host.mu.Lock()
	methods := strings.Join(s.host.methods, "\n")
	s.host.mu.Unlock()
	for _, w := range []string{
		"terminal/create agentflow ai respond af-session-7 ask-1 --allow",
		"terminal/create cat $AGENTFLOW_HOME/ai/sessions/af-session-7/answer.key",
	} {
		if !strings.Contains(methods, w) {
			t.Errorf("host never saw %q in\n%s", w, methods)
		}
	}
}

func TestSessionHangScriptCancels(t *testing.T) {
	s := launch(t, acptest.SessionHang(), func(h *host) { h.hold = true })
	id := s.start(t)
	type result struct {
		resp acp.PromptResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := s.prompt(t, id)
		done <- result{resp, err}
	}()
	select {
	case <-s.host.asked:
	case <-time.After(10 * time.Second):
		t.Fatal("the permission request never came")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := acp.Cancel(ctx, s.proc.Conn(), id); err != nil {
		t.Fatal(err)
	}
	// After session/cancel the host answers its held permission request
	// cancelled at once (the spec's MUST).
	close(s.host.release)
	select {
	case r := <-done:
		if r.err != nil || r.resp.StopReason != acp.StopCancelled {
			t.Fatalf("prompt: %+v %v", r.resp, r.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("prompt did not end after session/cancel")
	}
}

func TestSessionAuthScript(t *testing.T) {
	s := launch(t, acptest.SessionAuth(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := acp.Initialize(ctx, s.proc.Conn(), acp.SessionInitialize("1.0.0")); err != nil {
		t.Fatal(err)
	}
	_, err := acp.NewSession(ctx, s.proc.Conn(), acp.NewSessionRequest{Cwd: s.worktree})
	var rerr *jsonrpc.Error
	if !errors.As(err, &rerr) || rerr.Code != acp.CodeAuthRequired {
		t.Fatalf("err %v, want -32000", err)
	}
}

func TestSessionCrashScript(t *testing.T) {
	s := launch(t, &acptest.SessionScript{Steps: []acptest.Step{
		{Update: acptest.JSON(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "about to crash"}})},
		{Crash: true},
	}}, nil)
	_, err := s.prompt(t, s.start(t))
	if !errors.Is(err, jsonrpc.ErrClosed) {
		t.Fatalf("err %v, want ErrClosed", err)
	}
	select {
	case <-s.proc.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("the fake did not exit")
	}
	if s.proc.ExitCode() != 4 {
		t.Errorf("exit code %d", s.proc.ExitCode())
	}
}

func TestSessionScriptStepsAndPlaceholders(t *testing.T) {
	s := launch(t, &acptest.SessionScript{
		SessionID: "custom",
		Modes:     &acp.SessionModeState{CurrentModeID: "ask", AvailableModes: []acp.SessionMode{{ID: "ask", Name: "Ask"}, {ID: "code", Name: "Code"}}},
		Steps: []acptest.Step{
			{Ask: &acptest.Ask{Wait: json.RawMessage(`{}`)}}, // no terminal yet: skipped
			{Sleep: "20ms"},
			{Ask: &acptest.Ask{Read: json.RawMessage(`{"path":"notes.txt"}`)}},     // relative: refused by the host
			{Ask: &acptest.Ask{Read: json.RawMessage(`{"path":"{cwd}/missing"}`)}}, // -32002
			{ToolUnasked: acptest.JSON(map[string]any{"toolCallId": "t1", "title": "Edit", "kind": "edit", "locations": []map[string]any{{"path": "/etc/passwd"}}})},
			{Stop: acp.StopRefusal},
			{Update: acptest.JSON(map[string]any{"sessionUpdate": "agent_message_chunk"})}, // never played
		},
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := acp.Initialize(ctx, s.proc.Conn(), acp.SessionInitialize("1.0.0")); err != nil {
		t.Fatal(err)
	}
	ns, err := acp.NewSession(ctx, s.proc.Conn(), acp.NewSessionRequest{Cwd: s.worktree})
	if err != nil || ns.SessionID != "custom" || ns.Modes == nil || len(ns.Modes.AvailableModes) != 2 {
		t.Fatalf("session/new: %+v %v", ns, err)
	}
	if err := acp.SetMode(ctx, s.proc.Conn(), acp.SetSessionModeRequest{SessionID: "custom", ModeID: "code"}); err != nil {
		t.Fatal(err)
	}
	resp, err := s.prompt(t, "custom")
	if err != nil || resp.StopReason != acp.StopRefusal {
		t.Fatalf("prompt: %+v %v", resp, err)
	}
	asks := s.asks(t)
	if len(asks) != 3 || !asks[0].Skipped || asks[1].ErrData != "relative_path" || asks[2].Code != acp.CodeResourceNotFound {
		t.Fatalf("asks %+v", asks)
	}
	s.host.mu.Lock()
	defer s.host.mu.Unlock()
	if len(s.host.updates) != 2 {
		t.Fatalf("updates %+v", s.host.updates)
	}
	tc := s.host.updates[0].Update.ToolCall
	if tc == nil || tc.Kind != acp.ToolEdit || tc.Locations[0].Path != "/etc/passwd" {
		t.Errorf("tool_unasked %+v", tc)
	}
}

func TestEveryModeScriptIsCovered(t *testing.T) {
	for _, mode := range []string{"session-demo", "session-nested", "session-hang", "session-auth"} {
		if f, ok := acptest.SessionModeScripts[mode]; !ok || f() == nil {
			t.Errorf("no script for %s", mode)
		}
	}
	if len(acptest.SessionModeScripts) != 4 {
		t.Errorf("%d mode scripts", len(acptest.SessionModeScripts))
	}
}
