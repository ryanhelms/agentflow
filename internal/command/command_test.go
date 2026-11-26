package command_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
	"github.com/ryanhelms/agentflow/internal/policy"
	"github.com/ryanhelms/agentflow/internal/provider/native"
)

// fakeIn and fakeOut stand for an operation's MCP input and applied result.
type fakeIn struct {
	Message      string `json:"message"`
	DryRun       bool   `json:"dry_run,omitempty"`
	ConfirmToken string `json:"confirm_token,omitempty"`
}

type fakeOut struct {
	Status string `json:"status"`
	SHA    string `json:"sha"`
}

const approvalID = "op_00112233445566778899aabbccddeeff"

// fakeCommit behaves like a GateInRun write: without a token it asks for
// confirmation, with the token it applies, and an "invalid" message is
// refused with a status map.
func fakeCommit(runs *[]string) command.Spec[fakeIn, fakeOut] {
	return command.Spec[fakeIn, fakeOut]{
		Name:    "commit",
		CLIPath: []string{"commit"},
		CLI: &command.CLI[fakeIn]{
			Flags: func(fs *flag.FlagSet, in *fakeIn) {
				fs.StringVar(&in.Message, "m", "", "message")
				fs.StringVar(&in.ConfirmToken, "confirm-token", "", "token")
				fs.BoolVar(&in.DryRun, "dry-run", false, "preview")
			},
			Confirm: true,
			Usage:   "  commit [-m MSG] [--yes]         commit",
			Section: command.SectionWork,
			Order:   500,
		},
		MCP:      &command.MCP{Tool: "agentflow_commit", Description: "commit"},
		Policy:   command.Policy{Class: policy.Local, Action: "commit"},
		Token:    func(in *fakeIn) *string { return &in.ConfirmToken },
		DryRun:   func(in fakeIn) bool { return in.DryRun },
		Validate: func(in fakeIn) error { return map[bool]error{true: errors.New("-m is required")}[in.Message == ""] },
		Describe: func(in fakeIn) command.Detail { return command.Detail{Subject: in.Message, Branch: "main"} },
		Run: func(_ context.Context, _ *app.App, in fakeIn) (any, error) {
			*runs = append(*runs, in.ConfirmToken)
			switch {
			case in.Message == "invalid":
				return map[string]any{"status": "invalid"}, nil
			case in.DryRun:
				return map[string]any{"status": "valid"}, nil
			case in.ConfirmToken == "":
				return &app.Pending{Status: "needs_confirmation", ConfirmToken: approvalID, Reason: "confirm",
					Approval: &policy.Approval{OperationID: approvalID, Action: "commit", Class: policy.Local}}, nil
			}
			return fakeOut{Status: "committed", SHA: "abc123"}, nil
		},
		Links: func(o fakeOut) []bus.OpLink { return []bus.OpLink{{Kind: bus.OpLinkCommit, ID: o.SHA}} },
	}
}

func newInvoker(em *commandtest.Emitter, au *commandtest.Auditor) command.Invoker {
	return commandtest.Invoker{Options: command.Options{Emitters: em.Source(), Auditor: au}}
}

func TestCLIConfirmFlowPublishesOneOperation(t *testing.T) {
	var runs []string
	em, au := &commandtest.Emitter{}, &commandtest.Auditor{}
	env := &commandtest.Env{Answer: true}
	s := fakeCommit(&runs)
	if err := s.RunCLI(context.Background(), env, newInvoker(em, au), []string{"-m", "feat: add x\n\nsecret body"}); err != nil {
		t.Fatal(err)
	}
	// The output is the applied result, exactly as printJSON writes it.
	var want bytes.Buffer
	enc := json.NewEncoder(&want)
	enc.SetIndent("", "  ")
	_ = enc.Encode(fakeOut{Status: "committed", SHA: "abc123"})
	if env.Out.String() != want.String() {
		t.Fatalf("output %q, want %q", env.Out.String(), want.String())
	}
	if strings.Join(runs, ",") != ","+approvalID {
		t.Fatalf("runs %q", runs)
	}
	got := em.Types()
	wantTypes := []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval, bus.TypeOpRequested, bus.TypeOpApplied}
	if len(got) != len(wantTypes) {
		t.Fatalf("events %v", got)
	}
	evs := em.Events()
	for i, typ := range wantTypes {
		if got[i] != typ {
			t.Fatalf("event %d is %s, want %s (all: %v)", i, got[i], typ, got)
		}
		if evs[i].Worktree != commandtest.Worktree || evs[i].Source != "cli" {
			t.Errorf("event %d: worktree %q source %q", i, evs[i].Worktree, evs[i].Source)
		}
		if strings.Contains(string(evs[i].Payload), "secret body") {
			t.Errorf("event %d carries the message body: %s", i, evs[i].Payload)
		}
	}
	// The first request starts with a provisional ID; its outcome moves the
	// operation to the approval's ID and names the request as its cause.
	if evs[0].CorrelationID == approvalID || !strings.HasPrefix(evs[0].CorrelationID, "op_") {
		t.Errorf("first request correlation %q", evs[0].CorrelationID)
	}
	if evs[1].CorrelationID != approvalID || evs[1].CausationID != evs[0].ID {
		t.Errorf("awaiting: correlation %q causation %q", evs[1].CorrelationID, evs[1].CausationID)
	}
	if evs[2].CorrelationID != approvalID || evs[3].CorrelationID != approvalID || evs[3].CausationID != evs[2].ID {
		t.Errorf("resumed call not correlated: %+v", evs[2:])
	}
	p, _ := commandtest.Payload(evs[1])
	if p.Status != "needs_confirmation" || p.ApprovalID != approvalID || p.Subject != "feat: add x" || p.Class != "local" || p.Action != "commit" {
		t.Errorf("awaiting payload %+v", p)
	}
	if p, _ := commandtest.Payload(evs[2]); !p.Resumed {
		t.Error("resumed request not marked")
	}
	if p, _ := commandtest.Payload(evs[3]); len(p.Links) != 1 || p.Links[0].ID != "abc123" {
		t.Errorf("applied payload %+v", p)
	}
	// GateInRun: the app method writes its own audit line; the Invoker adds none.
	if lines := au.Lines(); len(lines) != 0 {
		t.Errorf("invoker audited a GateInRun operation: %+v", lines)
	}
}

func TestRefusalsDryRunsAndValidation(t *testing.T) {
	var runs []string
	cases := []struct {
		name  string
		args  []string
		types []bus.Type
		code  string
		err   bool
	}{
		{"refused by status", []string{"-m", "invalid", "--yes"}, []bus.Type{bus.TypeOpRequested, bus.TypeOpFailed}, "invalid", false},
		{"dry run", []string{"-m", "x", "--dry-run", "--confirm-token", approvalID}, nil, "", false},
		{"invalid input", nil, nil, "", true},
	}
	for _, c := range cases {
		em := &commandtest.Emitter{}
		env := &commandtest.Env{}
		err := fakeCommit(&runs).RunCLI(context.Background(), env, newInvoker(em, &commandtest.Auditor{}), c.args)
		if (err != nil) != c.err {
			t.Errorf("%s: err %v", c.name, err)
		}
		if got := em.Types(); len(got) != len(c.types) || (len(got) > 0 && got[len(got)-1] != c.types[len(c.types)-1]) {
			t.Errorf("%s: events %v, want %v", c.name, got, c.types)
		}
		if c.code != "" {
			evs := em.Events()
			if p, _ := commandtest.Payload(evs[len(evs)-1]); p.Code != c.code {
				t.Errorf("%s: code %q", c.name, p.Code)
			}
		}
	}
}

func TestClassify(t *testing.T) {
	exp := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	pending := func(status string) *app.Pending {
		return &app.Pending{Status: status, Approval: &policy.Approval{OperationID: approvalID, ExpiresAt: exp}}
	}
	cases := []struct {
		name string
		v    any
		err  error
		kind command.OutcomeKind
		code string
	}{
		{"applied value", fakeOut{SHA: "a"}, nil, command.OutcomeApplied, ""},
		{"applied pointer", &fakeOut{SHA: "a"}, nil, command.OutcomeApplied, ""},
		{"nil result", nil, nil, command.OutcomeApplied, ""},
		{"suggest", &app.Pending{Status: "suggest"}, nil, command.OutcomeSuggested, ""},
		{"confirm", pending("needs_confirmation"), nil, command.OutcomeAwaitingApproval, ""},
		{"shared approval", pending("awaiting_approval"), nil, command.OutcomeAwaitingApproval, ""},
		{"prepared", pending("prepared"), nil, command.OutcomePrepared, ""},
		{"unknown pending", &app.Pending{Status: "odd"}, nil, command.OutcomeFailed, command.CodeUnknownStatus},
		{"status map", map[string]any{"status": "blocked"}, nil, command.OutcomeFailed, "blocked"},
		{"status struct", struct {
			Status string `json:"status"`
		}{"invalid"}, nil, command.OutcomeFailed, "invalid"},
		{"other value", 42, nil, command.OutcomeFailed, command.CodeUnexpectedResult},
		{"policy error", nil, policy.ErrExpired, command.OutcomeFailed, "approval_expired"},
		{"cancelled", nil, context.Canceled, command.OutcomeFailed, command.CodeCancelled},
		{"plain error", nil, errors.New("/home/me/secret path"), command.OutcomeFailed, command.CodeError},
		{"native read", nil, &native.Request{Status: "native_read_required", ID: "r1"}, command.OutcomeDeferred, ""},
	}
	for _, c := range cases {
		out := command.Classify[fakeOut](c.v, c.err, nil)
		if out.Kind != c.kind || out.Code != c.code {
			t.Errorf("%s: %+v", c.name, out)
		}
	}
	if out := command.Classify[fakeOut](pending("prepared"), nil, nil); out.ApprovalID != approvalID || !out.ExpiresAt.Equal(exp) {
		t.Errorf("approval not carried: %+v", out)
	}
	if out := command.Classify[any](42, nil, nil); out.Kind != command.OutcomeApplied {
		t.Errorf("Out=any: %+v", out)
	}
}

func TestBindCorrelation(t *testing.T) {
	var runs []string
	s := fakeCommit(&runs)
	now := time.Now()
	fresh := command.Bind(nil, s, fakeIn{Message: "m"}, command.Origin{}, "", now)
	if !strings.HasPrefix(fresh.ID, "op_") || len(fresh.ID) != 35 || fresh.Resumed {
		t.Errorf("fresh %+v", fresh.Invocation)
	}
	resumed := command.Bind(nil, s, fakeIn{Message: "m", ConfirmToken: approvalID}, command.Origin{}, "", now)
	if resumed.ID != approvalID || !resumed.Resumed {
		t.Errorf("resumed %+v", resumed.Invocation)
	}
	bogus := command.Bind(nil, s, fakeIn{Message: "m", ConfirmToken: "nonsense"}, command.Origin{}, "op_ffffffffffffffffffffffffffffffff", now)
	if bogus.ID != "op_ffffffffffffffffffffffffffffffff" || !bogus.Resumed {
		t.Errorf("bogus token %+v", bogus.Invocation)
	}
}

func TestReadsPublishNothing(t *testing.T) {
	em := &commandtest.Emitter{}
	s := command.Spec[struct{}, any]{
		Name: "status", CLIPath: []string{"status"}, Policy: command.Policy{Class: policy.Read},
		CLI: &command.CLI[struct{}]{Usage: "  status                         status"},
		Run: func(context.Context, *app.App, struct{}) (any, error) { return map[string]any{"ok": true}, nil },
	}
	if m := s.Meta(); m.Events != command.ScopeNone || m.Gate != command.GateNone {
		t.Fatalf("read meta %+v", m)
	}
	env := &commandtest.Env{}
	if err := s.RunCLI(context.Background(), env, newInvoker(em, &commandtest.Auditor{}), nil); err != nil {
		t.Fatal(err)
	}
	if len(em.Events()) != 0 || !strings.Contains(env.Out.String(), `"ok": true`) {
		t.Fatalf("events %v, output %q", em.Types(), env.Out.String())
	}
}

func TestRegistryRules(t *testing.T) {
	var runs []string
	r := command.NewRegistry()
	if err := r.Add(fakeCommit(&runs)); err != nil {
		t.Fatal(err)
	}
	prepare := fakeCommit(&runs)
	prepare.Name, prepare.CLI, prepare.MCP = "commit.prepare", nil, &command.MCP{Tool: "agentflow_commit_prepare"}
	if err := r.Add(prepare); err != nil {
		t.Fatalf("a Spec may name another Spec's path without binding it: %v", err)
	}
	bad := map[string]func(*command.Spec[fakeIn, fakeOut]){
		"duplicate name":     func(s *command.Spec[fakeIn, fakeOut]) { s.MCP.Tool = "agentflow_other" },
		"duplicate tool":     func(s *command.Spec[fakeIn, fakeOut]) { s.Name, s.CLI = "commit.two", nil },
		"duplicate CLI path": func(s *command.Spec[fakeIn, fakeOut]) { s.Name, s.MCP = "commit.three", nil },
		"tool without CLI": func(s *command.Spec[fakeIn, fakeOut]) {
			s.Name, s.CLIPath, s.CLI, s.MCP.Tool = "x", nil, nil, "agentflow_x"
		},
		"bad name": func(s *command.Spec[fakeIn, fakeOut]) { s.Name = "Commit" },
		"bad tool": func(s *command.Spec[fakeIn, fakeOut]) { s.Name, s.CLI, s.MCP.Tool = "y", nil, "commit_y" },
		"write without action": func(s *command.Spec[fakeIn, fakeOut]) {
			s.Name, s.CLI, s.MCP.Tool, s.Policy.Action = "z", nil, "agentflow_z", ""
		},
		"CLI without usage": func(s *command.Spec[fakeIn, fakeOut]) {
			s.Name, s.CLIPath, s.MCP, s.CLI = "w", []string{"w"}, nil, &command.CLI[fakeIn]{}
		},
	}
	for name, mutate := range bad {
		s := fakeCommit(&runs)
		mutate(&s)
		if err := r.Add(s); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	verb := command.Verb{Path: []string{"pr"}, Usage: "  pr                             open a PR", Section: command.SectionWork, Order: 600,
		Run: func(context.Context, command.CLIEnv, command.Invoker, []string) error { return nil }}
	if err := r.AddVerb(verb); err != nil {
		t.Fatal(err)
	}
	if err := r.AddVerb(command.Verb{Path: []string{"commit"}, Usage: "  commit", Run: verb.Run}); err == nil {
		t.Error("verb over a bound path accepted")
	}
	if _, rest, ok := r.Dispatch([]string{"pr", "--draft"}); !ok || len(rest) != 1 {
		t.Errorf("dispatch pr: %v %v", rest, ok)
	}
	if _, _, ok := r.Dispatch([]string{"status"}); ok {
		t.Error("unregistered path dispatched")
	}
	rows := r.ParityRows()
	if rows["agentflow_commit"] != "commit" || rows["agentflow_commit_prepare"] != "commit" || len(rows) != 2 {
		t.Errorf("parity rows %v", rows)
	}
	usage := r.Usage()
	if len(usage) != 2 || usage[0].Path != "commit" || usage[1].Path != "pr" {
		t.Errorf("usage %+v", usage)
	}
}

func TestSortUsage(t *testing.T) {
	blocks := []command.UsageBlock{
		{Path: "mcp", Section: command.SectionRuntime, Order: 100},
		{Path: "b", Section: command.SectionWork, Order: 200},
		{Path: "a", Section: command.SectionWork, Order: 200},
		{Path: "start", Section: command.SectionWork, Order: 100},
		{Path: "init", Section: command.SectionSetup, Order: 900},
		{Path: "odd", Section: "Other"},
	}
	command.SortUsage(blocks)
	var got []string
	for _, b := range blocks {
		got = append(got, b.Path)
	}
	if strings.Join(got, ",") != "init,start,a,b,mcp,odd" {
		t.Errorf("order %v", got)
	}
}

func TestAddMCPRunsThroughTheInvoker(t *testing.T) {
	ctx := context.Background()
	var runs []string
	em := &commandtest.Emitter{}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	wrap := func(ctx context.Context, m command.Meta, call func(context.Context, *app.App, command.Origin) (any, error)) *mcp.CallToolResult {
		out, err := call(ctx, nil, command.Origin{Source: "mcp:claude", Client: "claude"})
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: m.Tool + " " + string(b)}}}
	}
	fakeCommit(&runs).AddMCP(srv, wrap, newInvoker(em, &commandtest.Auditor{}))
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	resp, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "agentflow_commit", Arguments: map[string]any{"message": "fix: y", "confirm_token": approvalID}})
	if err != nil {
		t.Fatal(err)
	}
	text := resp.Content[0].(*mcp.TextContent).Text
	if !strings.HasPrefix(text, "agentflow_commit {") || !strings.Contains(text, `"sha": "abc123"`) {
		t.Fatalf("tool result %q", text)
	}
	evs := em.Events()
	if len(evs) != 2 || evs[1].Type != bus.TypeOpApplied || evs[1].Source != "mcp:claude" || evs[1].CorrelationID != approvalID {
		t.Fatalf("events %+v", evs)
	}
	if p, _ := commandtest.Payload(evs[0]); p.Client != "claude" || !p.Resumed {
		t.Errorf("requested payload %+v", p)
	}
}

// auditCount runs a write once through the reference Invoker and returns
// how many audit lines the app method and the Invoker wrote together.
func auditCount(t *testing.T, invokerAudits, methodAudits bool) int {
	t.Helper()
	au := &commandtest.Auditor{}
	method := 0
	s := command.Spec[fakeIn, fakeOut]{
		Name: "op", Policy: command.Policy{Class: policy.Local, Action: "commit", AuditByInvoker: invokerAudits},
		Run: func(context.Context, *app.App, fakeIn) (any, error) {
			if methodAudits {
				method++ // stands for the method's own a.audit call
			}
			return fakeOut{Status: "ok"}, nil
		},
	}
	if _, err := command.Invoke(context.Background(), newInvoker(&commandtest.Emitter{}, au), nil, s, fakeIn{Message: "m"}, command.Origin{Source: "cli", Client: "cli"}); err != nil {
		t.Fatal(err)
	}
	return method + len(au.Lines())
}

func TestAuditLineCountUnchanged(t *testing.T) {
	// An operation that audits itself today keeps exactly one line.
	if n := auditCount(t, false, true); n != 1 {
		t.Errorf("self-auditing operation: %d lines", n)
	}
	// One that has no audit of its own gets exactly one, from the Invoker.
	if n := auditCount(t, true, false); n != 1 {
		t.Errorf("invoker-audited operation: %d lines", n)
	}
}

func TestNativeReadStepPublishesNothing(t *testing.T) {
	em, au := &commandtest.Emitter{}, &commandtest.Auditor{}
	s := command.Spec[fakeIn, fakeOut]{
		Name: "transition", NativeReads: true,
		Policy: command.Policy{Class: policy.Remote, Action: "transition", AuditByInvoker: true},
		Run: func(_ context.Context, _ *app.App, in fakeIn) (any, error) {
			if in.Message == "" {
				return nil, &native.Request{Status: "native_read_required", ID: "r1"}
			}
			return fakeOut{Status: "moved"}, nil
		},
	}
	inv := newInvoker(em, au)
	origin := command.Origin{Source: "mcp:claude", Client: "claude"}
	_, err := command.Invoke(context.Background(), inv, nil, s, fakeIn{}, origin)
	var nr *native.Request
	if !errors.As(err, &nr) {
		t.Fatalf("native request not returned unchanged: %v", err)
	}
	if len(em.Events()) != 0 || len(au.Lines()) != 0 {
		t.Fatalf("native read step published %v, audited %d", em.Types(), len(au.Lines()))
	}
	// The resumed call, with the read results, publishes the operation.
	if _, err := command.Invoke(context.Background(), inv, nil, s, fakeIn{Message: "results"}, origin); err != nil {
		t.Fatal(err)
	}
	evs := em.Events()
	if len(evs) != 2 || evs[0].Type != bus.TypeOpRequested || evs[1].Type != bus.TypeOpApplied ||
		evs[0].CorrelationID != evs[1].CorrelationID || evs[1].CausationID != evs[0].ID {
		t.Fatalf("resumed call events %+v", evs)
	}
	if len(au.Lines()) != 1 {
		t.Errorf("audit lines %d", len(au.Lines()))
	}
}
