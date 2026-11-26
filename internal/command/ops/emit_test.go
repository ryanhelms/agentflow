package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/commandtest"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/mcpserver"
	"github.com/ryanhelms/agentflow/internal/notifysock"
	"github.com/ryanhelms/agentflow/internal/policy"
)

// This file is the ADR's TestEveryWriteOperationEmitsEvents (section
// "Phase 3"). It walks the command registry; the three lists below name what
// the registry does not carry yet, each with the reason, so the phase 3
// cleanup PR can empty them (the test fails when an entry is no longer true).
// The same holds for directCalls and auditExempt.

// registeredWriteLegacyCLI lists the registered writes of this lane whose
// command line still runs the legacy code in internal/cli, so a call from the
// command line publishes no events yet. The tool of each is registered and
// emits them.
var registeredWriteLegacyCLI = map[string]string{
	"sweep":       "prints a table after the confirmation, and the CLI environment's confirmation flow prints the final result as JSON itself",
	"delegate":    "prints the plan as text after the confirmation, for the same reason as sweep",
	"setup.apply": "has its own prompt (Apply this setup action?) and refuses without a terminal; the CLI environment offers neither",
}

// laneWrites names the writes whose CLI binding this test enforces: those the
// lane that wrote the test migrated. The lanes migrating the other commands
// add their own names when they enforce the same rule.
var laneWrites = []string{
	"docs.publish", "adr.draft", "release.publish", "notify", "setup.apply", "worktree.go-work",
	"finish", "sweep", "delegate", "promote", "secops.triage",
}

// unregisteredWrites lists the CLI commands of this lane that write and are
// not in the registry at all: no Spec, so no events.
var unregisteredWrites = map[string]string{
	"config publish":       "its tool, agentflow_config_apply, takes a custom input schema (an enum on operation, raw JSON values) that command.MCP cannot carry yet; the usage path also holds a flag",
	"config save_personal": "writes the personal layer, outside any repository (ScopeNone); the usage path holds an underscore that no Spec can bind",
	"init":                 "writes the repository's files before there is an App to open; not an operation of this lane",
	"remove":               "undoes init, with no App for the same reason",
	"pin":                  "edits a workflow file through the plain file system; not an operation of this lane",
	"install":              "configures AI clients outside any repository (ScopeNone)",
	"login":                "stores a credential outside any repository (ScopeNone)",
	"logout":               "removes a credential outside any repository (ScopeNone)",
	"setup login":          "signs in to a provider through its CLI or MCP client, outside any repository (ScopeNone)",
	"setup logout":         "disconnects a provider enrollment, outside any repository (ScopeNone)",
	"release init":         "detects units and writes releases.yml and the release-please files; runs no gate and no audit",
	"release sync":         "regenerates the release-please files; runs no gate and no audit",
	"doctor":               "--fix repairs the installation; its --providers flag selects another command, so no Spec can bind the path",
}

// directCalls lists writes that a command-line flag runs as a direct app call:
// they have no tool and no Spec, so they publish no events. They are listed by
// the flag, not dispatched, because the command itself is registered.
var directCalls = map[string]string{
	"transition --github-event": "CI-only: the workflow hands the command the GitHub event, and the app method runs with no tool, no confirmation and no Spec",
	"transition --shipped":      "CI-only: the release workflow marks shipped tasks, with no tool, no confirmation and no Spec",
}

// auditExempt lists registered writes that neither set AuditByInvoker nor write
// an audit line on every path, each with the reason.
var auditExempt = map[string]string{}

// toolInputs gives the tools whose smallest input is a dry run the input
// that makes them write, so checkTool sees their events.
var toolInputs = map[string]map[string]any{
	"agentflow_commit_context": {"paths": []any{"a.txt"}}, // no paths only reads the staged index
}

// legacyOnlyTools lists the write tools of the MCP server that the registry
// cannot provide yet.
var legacyOnlyTools = map[string]string{
	"agentflow_config_apply": "its input schema is built from configconsole.ApplyRequest with an enum on operation; command.MCP has no custom schema, so a Spec would change the schema the tool golden file pins",
}

func isWrite(m command.Meta) bool { return m.Class == policy.Local || m.Class == policy.Remote }

func TestEveryWriteOperationEmitsEvents(t *testing.T) {
	f := newFixture(t)
	var writes []command.Command
	for _, c := range command.Default.Commands() {
		if isWrite(c.Meta()) {
			writes = append(writes, c)
		}
	}
	for _, name := range laneWrites {
		if c, ok := command.Default.Lookup(name); !ok || !isWrite(c.Meta()) {
			t.Fatalf("%s is not a registered write", name)
		}
	}
	for _, c := range writes {
		m := c.Meta()
		t.Run(m.Name, func(t *testing.T) {
			checkWriteRules(t, m)
			if m.Gate != command.GateByInvoker {
				checkOutcomes(t, f.app, m)
			}
			if m.Tool != "" {
				checkTool(t, f.app, c)
			}
		})
	}
	t.Run("legacy-only writes", func(t *testing.T) { checkLegacyOnly(t, f.dir) })
}

// selfAuditing lists the operations whose app method writes an audit line
// itself; every other write is audited by the Invoker.
var selfAuditing = map[string]bool{
	"eod.post": true, "docs.publish": true, "adr.draft": true, "release.publish": true, "notify": true,
	"setup.apply": true, "finish": true, "sweep": true, "delegate": true, "promote": true, "secops.triage": true,
	"config.set": true, "config.undo": true,
}

// checkWriteRules applies the rules that need only the Spec's metadata.
func checkWriteRules(t *testing.T, m command.Meta) {
	t.Helper()
	if m.Events == command.ScopeNone {
		t.Errorf("%s is a write with no events", m.Name)
	}
	if m.Gate != command.GateNone && !config.ValidAutonomyAction(m.Action) {
		t.Errorf("%s is gated under %q, which is not an autonomy action", m.Name, m.Action)
	}
	if _, exempt := auditExempt[m.Name]; m.Gate == command.GateNone && !m.AuditByInvoker && !exempt {
		t.Errorf("%s runs no gate and so no audit of its own: set Policy.AuditByInvoker", m.Name)
	}
	if audits, known := selfAuditing[m.Name]; known && m.AuditByInvoker == audits {
		t.Errorf("%s: AuditByInvoker is %v, but its app method audits itself: %v", m.Name, m.AuditByInvoker, audits)
	}
}

func pending(status, id string) *app.Pending {
	p := &app.Pending{Status: status, Reason: status, Summary: map[string]string{"what": "x"}}
	if status != string(policy.SuggestOnly) {
		p.ConfirmToken = "cf_" + id
		p.Approval = &policy.Approval{OperationID: id, State: "pending", Action: "x", Class: policy.Local, ExpiresAt: time.Now().Add(time.Minute)}
	}
	return p
}

// metaOut stands for the applied result type of an operation, so that a value
// with a status field is a refusal and not a result.
type metaOut struct{}

// metaCall builds the Call of an operation described by m, whose Run is run.
// Its subject has a second line, which no payload may keep.
func metaCall(a *app.App, m command.Meta, run func(context.Context) (any, error)) *command.Call {
	c := &command.Call{App: a}
	c.ID, c.Meta, c.Started = command.NewOperationID(), m, time.Now()
	c.Origin = command.Origin{Source: "cli", Client: "cli"}
	c.Detail = command.Detail{Subject: "subject\nsecond line", Task: "TASK-1", Branch: "task-1"}
	c.Validate = func() error { return nil }
	c.Run = run
	c.Classify = func(v any, err error) command.Outcome { return command.Classify[metaOut](v, err, nil) }
	return c
}

// checkOutcomes runs the operation with its Run replaced by stubs that return
// each outcome, through both Invokers: op.requested comes first, then the
// matching event, and every event passes the bus and the socket.
func checkOutcomes(t *testing.T, a *app.App, m command.Meta) {
	t.Helper()
	const id = "op_00112233445566778899aabbccddeeff"
	failure := errors.New("provider said secret-token at /private/tmp/path")
	for _, sc := range []struct {
		name string
		run  func(context.Context) (any, error)
		want bus.Type
		code string
	}{
		{"applied", func(context.Context) (any, error) { return metaOut{}, nil }, bus.TypeOpApplied, ""},
		{"needs confirmation", func(context.Context) (any, error) { return pending("needs_confirmation", id), nil }, bus.TypeOpAwaitingApproval, ""},
		{"awaiting approval", func(context.Context) (any, error) { return pending("awaiting_approval", id), nil }, bus.TypeOpAwaitingApproval, ""},
		{"suggest", func(context.Context) (any, error) { return pending("suggest", id), nil }, bus.TypeOpSuggested, ""},
		{"prepared", func(context.Context) (any, error) { return pending("prepared", id), nil }, bus.TypeOpPrepared, ""},
		{"refusal", func(context.Context) (any, error) { return map[string]any{"status": "blocked"}, nil }, bus.TypeOpFailed, "blocked"},
		{"error", func(context.Context) (any, error) { return nil, failure }, bus.TypeOpFailed, command.CodeError},
	} {
		for name, newInvoker := range map[string]func(command.Options) command.Invoker{
			"reference":  func(o command.Options) command.Invoker { return commandtest.Invoker{Options: o} },
			"production": command.NewInvoker,
		} {
			t.Run(sc.name+"/"+name, func(t *testing.T) {
				em, au := &commandtest.Emitter{}, &commandtest.Auditor{}
				inv := newInvoker(command.Options{Emitters: em.Source(), Auditor: au})
				if _, err := inv.Invoke(t.Context(), metaCall(a, m, sc.run)); !errors.Is(err, failureOf(sc.name, failure)) {
					t.Fatalf("Invoke returned %v", err)
				}
				evs := em.Events()
				if !slices.Equal(em.Types(), []bus.Type{bus.TypeOpRequested, sc.want}) {
					t.Fatalf("events %v, want op.requested then %s", em.Types(), sc.want)
				}
				for _, ev := range evs {
					if code := notifysock.Check(ev); code != "" {
						t.Errorf("%s would be refused by the socket: %s", ev.Type, code)
					}
					p, err := commandtest.Payload(ev)
					if err != nil {
						t.Fatal(err)
					}
					if p.Operation != m.Name || p.Action != m.Action || p.Class != string(m.Class) || p.Subject != "subject" {
						t.Errorf("%s payload %+v", ev.Type, p)
					}
					if strings.Contains(string(ev.Payload), "second line") || strings.Contains(string(ev.Payload), "secret-token") || strings.Contains(string(ev.Payload), "/private") {
						t.Errorf("%s payload holds a second line, error text or a path: %s", ev.Type, ev.Payload)
					}
					if wantWorktree := m.Events == command.ScopeWorktree; (ev.Worktree != "") != wantWorktree {
						t.Errorf("%s worktree %q for scope %v", ev.Type, ev.Worktree, m.Events)
					}
				}
				if p, _ := commandtest.Payload(evs[1]); p.Code != sc.code {
					t.Errorf("code %q, want %q", p.Code, sc.code)
				}
				if evs[1].CausationID != evs[0].ID {
					t.Errorf("the outcome does not name its request as cause")
				}
				if want := map[bool]int{true: 1, false: 0}[m.AuditByInvoker]; len(au.Lines()) != want {
					t.Errorf("%d audit lines from the Invoker, want %d", len(au.Lines()), want)
				}
			})
		}
	}
	t.Run("dry run", func(t *testing.T) {
		em := &commandtest.Emitter{}
		c := metaCall(a, m, func(context.Context) (any, error) { return map[string]any{}, nil })
		c.DryRun = true
		if _, err := command.NewInvoker(command.Options{Emitters: em.Source()}).Invoke(t.Context(), c); err != nil || len(em.Events()) != 0 {
			t.Errorf("a dry run published %v (%v)", em.Types(), err)
		}
	})
}

// failureOf is the error Invoke returns in scenario name.
func failureOf(name string, failure error) error {
	if name == "error" {
		return failure
	}
	return nil
}

// checkTool drives the operation's tool with the real Spec and a stubbed Run:
// the call carries a confirm token and a dry run exactly when the input
// schema offers them, and no payload carries the token.
func checkTool(t *testing.T, a *app.App, c command.Command) {
	t.Helper()
	m := c.Meta()
	var stub func(context.Context) (any, error)
	em := &commandtest.Emitter{}
	real := command.NewInvoker(command.Options{Emitters: em.Source()})
	inv := command.InvokerFunc(func(ctx context.Context, call *command.Call) (any, error) {
		call.Run = stub
		return real.Invoke(ctx, call)
	})
	cs, tool := connectTool(t, c, a, inv)
	var schema struct {
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	raw, _ := json.Marshal(tool.InputSchema)
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(raw, &root)
	call := func(extra map[string]any) {
		t.Helper()
		in := sampleObject(root)
		for k, v := range toolInputs[tool.Name] {
			in[k] = v
		}
		for k, v := range extra {
			in[k] = v
		}
		if _, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: in}); err != nil {
			t.Fatalf("call %v: %v", in, err)
		}
	}

	// A call that must ask for confirmation.
	stub = func(context.Context) (any, error) {
		return pending("needs_confirmation", "op_00112233445566778899aabbccddeeff"), nil
	}
	call(nil)
	evs := em.Events()
	if !slices.Equal(em.Types(), []bus.Type{bus.TypeOpRequested, bus.TypeOpAwaitingApproval}) {
		t.Fatalf("tool events %v", em.Types())
	}
	p, err := commandtest.Payload(evs[0])
	if err != nil {
		t.Fatal(err)
	}
	if p.Operation != m.Name || p.Client != "test" || len(p.Subject) > bus.MaxPayloadText {
		t.Errorf("request payload %+v", p)
	}
	if evs[0].Source != "mcp:test" {
		t.Errorf("source %q", evs[0].Source)
	}

	// A call that resumes with a token: the Spec says where the token is.
	if _, ok := schema.Properties["confirm_token"]; ok {
		token := "cf_" + strings.Repeat("r", 24)
		em2 := len(em.Events())
		stub = func(context.Context) (any, error) { return map[string]any{"ok": true}, nil }
		call(map[string]any{"confirm_token": token})
		evs = em.Events()[em2:]
		if len(evs) != 2 {
			t.Fatalf("resumed events %v", em.Types())
		}
		if p, _ := commandtest.Payload(evs[0]); !p.Resumed {
			t.Errorf("a call with confirm_token does not say it resumed: set Spec.Token (%+v)", p)
		}
		for _, ev := range evs {
			if strings.Contains(string(ev.Payload), token) {
				t.Errorf("%s payload carries the token", ev.Type)
			}
		}
	}

	// A dry run publishes nothing.
	if _, ok := schema.Properties["dry_run"]; ok {
		before := len(em.Events())
		stub = func(context.Context) (any, error) { return map[string]any{"ok": true}, nil }
		call(map[string]any{"dry_run": true})
		if got := len(em.Events()) - before; got != 0 {
			t.Errorf("a dry run published %d events: set Spec.DryRun", got)
		}
	}
}

// connectTool serves c's tool from an in-memory MCP server whose calls run on
// a with an MCP origin, and returns a client session and the tool.
func connectTool(t *testing.T, c command.Command, a *app.App, inv command.Invoker) (*mcp.ClientSession, *mcp.Tool) {
	t.Helper()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	wrap := func(ctx context.Context, _ command.Meta, call func(context.Context, *app.App, command.Origin) (any, error)) *mcp.CallToolResult {
		out, err := call(ctx, a, command.Origin{Source: "mcp:test", Client: "test"})
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
		}
		b, _ := json.Marshal(out)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
	}
	c.AddMCP(srv, wrap, inv)
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(t.Context(), st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil || len(list.Tools) != 1 {
		t.Fatalf("tools: %v, %v", list, err)
	}
	return cs, list.Tools[0]
}

// sampleObject returns a value for every required property of a JSON schema
// object, the smallest input the tool accepts.
func sampleObject(schema map[string]any) map[string]any {
	out := map[string]any{}
	props, _ := schema["properties"].(map[string]any)
	required, _ := schema["required"].([]any)
	for _, name := range required {
		key, _ := name.(string)
		prop, _ := props[key].(map[string]any)
		out[key] = sampleValue(prop)
	}
	return out
}

func sampleValue(schema map[string]any) any {
	if enum, ok := schema["enum"].([]any); ok && len(enum) > 0 {
		return enum[0]
	}
	typ := schema["type"]
	if list, ok := typ.([]any); ok {
		typ = nil
		for _, v := range list {
			if v != "null" {
				typ = v
				break
			}
		}
	}
	switch typ {
	case "boolean":
		return false
	case "integer", "number":
		return 1
	case "array":
		return []any{}
	case "object":
		return sampleObject(schema)
	}
	return "x"
}

// checkLegacyOnly holds the three lists above to the truth: every write tool
// is registered or listed, every listed command is still legacy, and every
// registered write of the lane that has no CLI binding is listed.
func checkLegacyOnly(t *testing.T, dir string) {
	t.Helper()
	for what, lists := range map[string]map[string]string{"legacyOnlyTools": legacyOnlyTools,
		"unregisteredWrites": unregisteredWrites, "registeredWriteLegacyCLI": registeredWriteLegacyCLI,
		"directCalls": directCalls, "auditExempt": auditExempt} {
		for name, reason := range lists {
			if strings.TrimSpace(reason) == "" {
				t.Errorf("%s: %q has no reason", what, name)
			}
		}
	}

	s, err := mcpserver.New(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(t.Context(), st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	served := map[string]bool{}
	for _, tool := range list.Tools {
		served[tool.Name] = true
		readOnly := tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		_, registered := command.Default.Tool(tool.Name)
		_, legacy := legacyOnlyTools[tool.Name]
		switch {
		case readOnly || registered:
		case !legacy:
			t.Errorf("write tool %s is neither a registered command nor listed in legacyOnlyTools", tool.Name)
		}
		if legacy && registered {
			t.Errorf("legacyOnlyTools lists %s, which the registry now provides: remove the entry", tool.Name)
		}
	}
	for name := range legacyOnlyTools {
		if !served[name] {
			t.Errorf("legacyOnlyTools lists %s, which the server does not offer: remove the entry", name)
		}
	}

	for name := range auditExempt {
		c, ok := command.Default.Lookup(name)
		switch {
		case !ok || !isWrite(c.Meta()):
			t.Errorf("auditExempt lists %s, which is not a registered write: remove the entry", name)
		case c.Meta().Gate != command.GateNone || c.Meta().AuditByInvoker:
			t.Errorf("auditExempt lists %s, which is now gated or audited by the Invoker: remove the entry", name)
		}
	}
	checkDirectCalls(t)

	for path := range unregisteredWrites {
		if _, _, ok := command.Default.Dispatch(strings.Fields(path)); ok {
			t.Errorf("unregisteredWrites lists %q, which the registry now runs: remove the entry", path)
		}
	}
	for _, name := range laneWrites {
		c, _ := command.Default.Lookup(name)
		_, _, bound := command.Default.Dispatch(c.Meta().CLIPath)
		_, listed := registeredWriteLegacyCLI[name]
		switch {
		case !bound && !listed:
			t.Errorf("%s has no CLI binding in the registry: bind it or list it in registeredWriteLegacyCLI", name)
		case bound && listed:
			t.Errorf("registeredWriteLegacyCLI lists %s, whose CLI the registry now runs: remove the entry", name)
		}
	}
	for name := range registeredWriteLegacyCLI {
		if !slices.Contains(laneWrites, name) {
			t.Errorf("registeredWriteLegacyCLI lists %s, which is not in laneWrites", name)
		}
	}
}

// checkDirectCalls holds directCalls to the truth: each command is
// registered, still declares its flag and, run with it, still calls its app
// method directly instead of the Invoker. The CI environment is cleared so the
// GitHub event mode stops before it reads an event.
func checkDirectCalls(t *testing.T) {
	t.Helper()
	for _, k := range []string{"GITHUB_REPOSITORY_ID", "GITHUB_EVENT_PATH", "GITHUB_EVENT_NAME"} {
		t.Setenv(k, "")
	}
	for path := range directCalls {
		args := strings.Fields(path)
		run, rest, ok := command.Default.Dispatch(args)
		if !ok || len(rest) == len(args) {
			t.Errorf("directCalls lists %q, whose command is not registered: remove the entry", path)
			continue
		}
		invoked := false
		inv := command.InvokerFunc(func(context.Context, *command.Call) (any, error) {
			invoked = true
			return nil, errors.New("invoked")
		})
		err := run(t.Context(), &commandtest.Env{}, inv, rest)
		switch {
		case err != nil && strings.Contains(err.Error(), "flag provided but not defined"):
			t.Errorf("directCalls lists %q, whose flag the command no longer has: remove the entry", path)
		case invoked:
			t.Errorf("directCalls lists %q, which now runs through the Invoker: remove the entry", path)
		}
	}
}
