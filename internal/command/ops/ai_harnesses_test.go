package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
	"github.com/ryanhelms/agentflow/internal/hooks"
)

// fakeDetector answers every pass with report and records what it was asked.
// No test here finds or starts a harness.
type fakeDetector struct {
	report harness.Report
	asked  []harness.DetectOptions
}

func (d *fakeDetector) Detect(_ context.Context, o harness.DetectOptions) (harness.Report, error) {
	d.asked = append(d.asked, o)
	return d.report, nil
}

// last returns what the latest pass was asked and forgets the passes.
func (d *fakeDetector) last(t *testing.T) harness.DetectOptions {
	t.Helper()
	if len(d.asked) != 1 {
		t.Fatalf("%d detection passes, want 1: %+v", len(d.asked), d.asked)
	}
	o := d.asked[0]
	d.asked = nil
	return o
}

// newHarnessRepo is newOpsRepo with a fake PATH, a fake Detector that
// reports report, and no console daemon.
func newHarnessRepo(t *testing.T, report harness.Report) (*opsRepo, *fakeDetector) {
	t.Helper()
	harnesstest.PATH(t)
	r := newOpsRepo(t, "agentflow: 1\n", confirmEverything)
	d := &fakeDetector{report: report}
	previousDetector, previousDaemon := app.NewHarnessDetector, ops.HarnessDaemonRunning
	app.NewHarnessDetector = func(*app.App) (harness.Detector, error) { return d, nil }
	ops.HarnessDaemonRunning = func(context.Context) (bool, error) { return false, nil }
	t.Cleanup(func() {
		app.NewHarnessDetector, ops.HarnessDaemonRunning = previousDetector, previousDaemon
	})
	return r, d
}

func sampleHarnessReport() harness.Report {
	now := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	return harness.Report{
		Registry: harness.RegistryInfo{Commit: "4273056f03b93f8ce41e9276256ea47b057b794c", CommittedAt: now.Add(-24 * time.Hour), Agents: 46},
		Harnesses: []harness.Harness{
			{ID: "gemini", Name: "Gemini CLI", State: harness.StateNative, Command: "gemini", Args: []string{"--acp"}, Version: "0.71.1",
				Probe: harness.Probe{Status: harness.ProbeOK, Code: harness.CodeOK},
				// Sign-in methods are guidance: listed only where sign-in is needed.
				SignIn: []harness.SignIn{{Method: "oauth", Type: "agent", Name: "Log in with Google"}}},
			{ID: "codex-acp", Name: "Codex", State: harness.StateNeedsAuth, Command: "codex-acp", Version: "2.12.1",
				Probe: harness.Probe{Status: harness.ProbeOK, Code: harness.CodeOK},
				Fix:   &harness.Fix{Text: "Sign in to Codex, then probe again.", Command: "codex login"},
				SignIn: []harness.SignIn{
					{Method: "chatgpt", Type: "agent", Name: "ChatGPT"},
					{Method: "device", Type: "terminal", Name: "Device code", Command: "codex-acp login --device"},
					{Method: "odd", Type: "terminal", Name: "Custom", Unsafe: true},
				}},
			{ID: "claude-acp", Name: "Claude Agent", State: harness.StateAdapterAvailable, Command: "claude-agent-acp",
				Probe: harness.Probe{Status: harness.ProbeNone, Code: harness.CodeNotProbed},
				Fix: &harness.Fix{Text: "Claude Code is installed, but its ACP adapter is not. Install the adapter yourself; agentflow never installs it.",
					Command: "npm install -g @agentclientprotocol/claude-agent-acp@1.0.0", URL: "https://github.com/agentclientprotocol/claude-agent-acp"}},
			// Text that tries to drive the terminal.
			{ID: "odd", Name: "Odd\x1b[31m", State: harness.StateIncompatible, Quarantined: true,
				QuarantineReason: "fails\u202e checks\r",
				Fix:              &harness.Fix{Text: "line one\nline\x1b]0;title\x07 two"}},
		},
		SkippedPath:  []harness.SkippedDir{{Dir: "~/app/bin", Reason: harness.SkipInsideRepository}},
		Problems:     []harness.Problem{{Layer: harness.SourcePersonal, ID: "my-agent", Code: "launcher", Message: "command: npx is never run"}},
		ProbeRefused: harness.CodeNeedsPerson,
		CheckedAt:    now,
	}
}

// aiRun runs an ai command line and returns what it printed, the JSON the
// Env got, and its error.
func aiRun(t *testing.T, r *opsRepo, args ...string) (text, jsonOut string, err error) {
	t.Helper()
	var out string
	text = taskStdout(t, func() {
		em, env, runErr := r.cli(t, false, args...)
		err, out = runErr, env.Out.String()
		if got := em.Types(); len(got) != 0 {
			t.Errorf("a read published %v", got)
		}
	})
	return text, out, err
}

func TestAIRequesterIsAPersonOnlyAtATerminalOutsideAnAgent(t *testing.T) {
	r, d := newHarnessRepo(t, harness.Report{})
	for _, command := range []string{"harnesses", "doctor"} {
		check := func(when, want string) {
			t.Helper()
			if _, _, err := aiRun(t, r, "ai", command, "--probe"); err != nil {
				t.Fatal(err)
			}
			if o := d.last(t); o.Requester != want || !o.Probe {
				t.Errorf("ai %s %s: %+v, want requester %s", command, when, o, want)
			}
		}
		// goldentest.Isolate gives the test an agent session.
		setTerminal(t, true)
		check("in an agent session at a terminal", harness.RequesterAgent)
		unsetAgentEnvironment(t)
		setTerminal(t, false)
		check("without a terminal", harness.RequesterAgent)
		setTerminal(t, true)
		check("as a person", harness.RequesterPerson)

		// A tool call is an agent's, even where the command line would be a
		// person's.
		_, inv := recording()
		if out, isErr := invokeTool(t, r.app, "ai."+command, map[string]any{"probe": true}, inv); isErr {
			t.Fatalf("tool: %s", out)
		}
		if o := d.last(t); o.Requester != harness.RequesterAgent || !o.Probe {
			t.Errorf("tool ai %s: %+v", command, o)
		}
		t.Setenv("CLAUDECODE", "1")
	}
}

// The console's Probe button invokes ai.harnesses by name and says a person
// asked on the context. The input cannot say it.
func TestAIRequesterComesFromTheCallingSurfaceNeverTheInput(t *testing.T) {
	r, d := newHarnessRepo(t, harness.Report{})
	c, ok := command.Default.Lookup("ai.harnesses")
	if !ok {
		t.Fatal("ai.harnesses is not registered")
	}
	jc := c.(command.JSONCommand)
	_, inv := recording()
	origin := command.Origin{Source: "daemon", Client: "repository-console"}
	input := json.RawMessage(`{"probe":true,"ids":["gemini"]}`)
	for ctx, want := range map[context.Context]string{
		// The client name alone makes nobody a person.
		t.Context(): harness.RequesterAgent,
		app.WithHarnessRequester(t.Context(), harness.RequesterPerson): harness.RequesterPerson,
	} {
		if res := jc.InvokeJSON(ctx, inv, r.app, input, "", origin); res.Err != nil {
			t.Fatal(res.Err)
		}
		if o := d.last(t); o.Requester != want || !o.Probe || !slices.Equal(o.IDs, []string{"gemini"}) {
			t.Errorf("InvokeJSON: %+v, want requester %s", o, want)
		}
	}
	res := jc.InvokeJSON(t.Context(), inv, r.app, json.RawMessage(`{"requester":"person"}`), "", origin)
	if !errors.Is(res.Err, command.ErrJSONInput) {
		t.Errorf("a requester in the input: %v", res.Err)
	}
	for _, name := range []string{"ai.harnesses", "ai.doctor"} {
		if out, isErr := invokeTool(t, r.app, name, map[string]any{"requester": "person"}, inv); !isErr {
			t.Errorf("%s took a requester: %s", name, out)
		}
	}
	if len(d.asked) != 0 {
		t.Errorf("refused inputs still detected: %+v", d.asked)
	}
}

func TestAIHarnessesInputs(t *testing.T) {
	r, d := newHarnessRepo(t, harness.Report{})
	want := harness.DetectOptions{All: true, Probe: true, IDs: []string{"gemini", "goose"}, Requester: harness.RequesterAgent}
	same := func(o harness.DetectOptions) bool {
		return o.All == want.All && o.Probe == want.Probe && o.Requester == want.Requester && slices.Equal(o.IDs, want.IDs)
	}
	if _, _, err := aiRun(t, r, "ai", "harnesses", "--probe", "--all", "--id", "gemini", "--id", "goose"); err != nil {
		t.Fatal(err)
	}
	if o := d.last(t); !same(o) {
		t.Errorf("flags gave %+v", o)
	}
	_, inv := recording()
	out, isErr := invokeTool(t, r.app, "ai.harnesses", map[string]any{"probe": true, "all": true, "ids": []string{"gemini", "goose"}}, inv)
	if isErr {
		t.Fatal(out)
	}
	if o := d.last(t); !same(o) {
		t.Errorf("tool gave %+v", o)
	}

	for _, args := range [][]string{
		{"ai", "harnesses", "--id", "../x"},
		{"ai", "harnesses", "gemini"},
		{"ai", "doctor", "gemini"},
	} {
		if _, _, err := aiRun(t, r, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
	if len(d.asked) != 0 {
		t.Errorf("refused inputs still detected: %+v", d.asked)
	}
}

func TestAIHarnessesTextJSONAndToolAgree(t *testing.T) {
	r, d := newHarnessRepo(t, sampleHarnessReport())
	text, _, err := aiRun(t, r, "ai", "harnesses")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(text, "\x1b\x07\r\u202e") {
		t.Errorf("control characters reach the terminal: %q", text)
	}
	goldentest.Check(t, "ai/harnesses.txt", text)

	_, jsonOut, err := aiRun(t, r, "ai", "harnesses", "--json")
	if err != nil {
		t.Fatal(err)
	}
	_, inv := recording()
	toolOut, isErr := invokeTool(t, r.app, "ai.harnesses", map[string]any{}, inv)
	if isErr {
		t.Fatal(toolOut)
	}
	var fromCLI, fromTool harness.Report
	if err := json.Unmarshal([]byte(jsonOut), &fromCLI); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(toolOut), &fromTool); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fromCLI)
	b, _ := json.Marshal(fromTool)
	if string(a) != string(b) || len(fromCLI.Harnesses) != 4 {
		t.Errorf("--json and the tool differ:\n%s\n%s", a, b)
	}

	d.report = harness.Report{}
	text, _, err = aiRun(t, r, "ai", "harnesses")
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Check(t, "ai/harnesses_none.txt", text)
	if _, jsonOut, err = aiRun(t, r, "ai", "harnesses", "--json"); err != nil || !strings.Contains(jsonOut, `"harnesses": []`) {
		t.Errorf("empty report: %s, %v", jsonOut, err)
	}
	if lines := r.auditLines(t); len(lines) != 0 {
		t.Errorf("reads wrote %d audit lines", len(lines))
	}
}

func TestAIDoctorExitsOneOnlyWhenACheckFails(t *testing.T) {
	report := sampleHarnessReport()
	report.Problems = nil
	r, d := newHarnessRepo(t, report)

	// Warnings only: exit 0.
	text, _, err := aiRun(t, r, "ai", "doctor")
	if err != nil {
		t.Fatalf("warnings only: %v", err)
	}
	goldentest.Check(t, "ai/doctor_warn.txt", text)

	d.report = sampleHarnessReport()
	exitOne := func(err error) {
		t.Helper()
		var ee *hooks.ExitError
		if !errors.As(err, &ee) || ee.Code != 1 || ee.Msg != "" {
			t.Errorf("error %#v, want a silent exit 1", err)
		}
	}
	text, _, err = aiRun(t, r, "ai", "doctor")
	exitOne(err)
	if strings.ContainsAny(text, "\x1b\x07\r\u202e") {
		t.Errorf("control characters reach the terminal: %q", text)
	}
	goldentest.Check(t, "ai/doctor_fail.txt", text)

	_, jsonOut, err := aiRun(t, r, "ai", "doctor", "--json")
	exitOne(err)
	var doctor app.DoctorReport
	if err := json.Unmarshal([]byte(jsonOut), &doctor); err != nil || !doctor.Failed() || len(doctor.Report.Harnesses) != 4 {
		t.Errorf("--json: %v %s", err, jsonOut)
	}

	// The tool returns the report: a failed check is data, not a tool error.
	_, inv := recording()
	out, isErr := invokeTool(t, r.app, "ai.doctor", map[string]any{}, inv)
	if isErr || !strings.Contains(out, `"id":"definitions","status":"fail"`) {
		t.Errorf("tool: %v %s", isErr, out)
	}
	if lines := r.auditLines(t); len(lines) != 0 {
		t.Errorf("reads wrote %d audit lines", len(lines))
	}
}
