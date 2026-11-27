package webui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
	"github.com/ryanhelms/agentflow/internal/watch"
)

// TestHarnessProductionWiring runs the production wiring end to end: the
// real Detector, cache.json, ACP prober and sign-in check from
// harness.Production, as the CLI (app.NewHarnessDetector) and the console
// daemon (productionHarnessOptions) build them, over a fake agent and a
// fake status CLI in temporary directories.
//
//   - An agent's probe while auto_probe is off runs nothing.
//   - A person's probe while sign_in_check is off runs the probe but no
//     status command; with it on, the status command runs once and its
//     output is kept nowhere.
//   - The daemon's pass and a console probe through the daemon's Detector
//     report what the CLI reported.
func TestHarnessProductionWiring(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake status CLI is a /bin/sh script")
	}
	// A credential variable in the parent would turn signed_out into
	// unknown/env_credential; this test checks the plain verdict.
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); harnessdef.CredentialSuffix(name) || strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_CODE_") {
			t.Setenv(name, "")
		}
	}
	bin := t.TempDir()
	home := harnesstest.PATH(t, bin)
	agentLog := filepath.Join(t.TempDir(), "agent.log")
	args := acptest.Install(t, bin, "fake-acp", acptest.Script{Log: agentLog})
	const secret = "status-output-never-kept"
	status := harnesstest.StatusCLI(t, bin, "fake-cli", 1, secret)
	adapter := harnessdef.Definition{Name: "Demo sign-in", Kind: harnessdef.KindAdapter, Command: "fake-acp", Args: args,
		BaseName: "Demo CLI", BaseCommands: []string{"fake-cli"}, StatusFrom: harnessdef.StatusFromBase, StatusArgs: []string{"status"}}
	overrides := map[string]harnessdef.Definition{
		"demo-ready":      {Name: "Demo ready", Command: "fake-acp", Args: args},
		"demo-signin":     adapter,
		"demo-signin-off": adapter,
	}
	settings := func(auto, signIn bool) {
		harnesstest.WriteSettings(t, home, harnesstest.Settings{AutoProbe: auto, SignInCheck: signIn, Overrides: overrides})
	}
	starts := func() int {
		entries, err := acptest.ReadLog(agentLog)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range entries {
			if e.Method == "initialize" {
				n++
			}
		}
		return n
	}
	statusRuns := func() int {
		calls, err := status.Calls()
		if err != nil {
			t.Fatal(err)
		}
		return len(calls)
	}
	a := &app.App{Home: home, Repo: &gitx.Repo{Dir: t.TempDir()}}
	cli := func(requester string, probe bool, ids ...string) harness.Report {
		t.Helper()
		ctx := app.WithHarnessRequester(context.Background(), requester)
		r, err := a.Harnesses(ctx, app.HarnessQuery{Probe: probe, IDs: ids})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	byID := func(r harness.Report, id string) harness.Harness {
		t.Helper()
		for _, h := range r.Harnesses {
			if h.ID == id {
				return h
			}
		}
		t.Fatalf("%s not in %+v", id, r.Harnesses)
		return harness.Harness{}
	}

	// An agent asks for a probe while auto_probe is off: nothing runs.
	settings(false, true)
	r := cli(harness.RequesterAgent, true)
	if r.ProbeRefused != harness.CodeNeedsPerson || byID(r, "demo-ready").Probe.Status != harness.ProbeRefused {
		t.Fatalf("agent probe with auto_probe off: refused %q, %+v", r.ProbeRefused, byID(r, "demo-ready").Probe)
	}
	if n, s := starts(), statusRuns(); n != 0 || s != 0 {
		t.Fatalf("an agent's refused probe started %d agents and %d status commands", n, s)
	}
	if _, err := os.Stat(filepath.Join(home, "harnesses", "demo-ready", "probe.log")); !os.IsNotExist(err) {
		t.Fatalf("a refused probe left a probe.log: %v", err)
	}

	// A person probes while sign_in_check is off: the probe runs, the
	// status command does not.
	settings(false, false)
	r = cli(harness.RequesterPerson, true, "demo-ready", "demo-signin-off")
	if h := byID(r, "demo-ready"); h.State != harness.StateNative || h.Probe.Status != harness.ProbeOK || h.Version != "1.2.3" {
		t.Fatalf("demo-ready = %+v", h)
	}
	if h := byID(r, "demo-signin-off"); h.State != harness.StateAdapterInstalled || h.Auth.Status != harness.SignInUnknown || h.Auth.Code != harness.CodeOff {
		t.Fatalf("demo-signin-off = %+v %+v", h.State, h.Auth)
	}
	if n, s := starts(), statusRuns(); n != 2 || s != 0 {
		t.Fatalf("with sign_in_check off: %d probes, %d status commands", n, s)
	}

	// With sign_in_check on, a person's probe runs the status command once.
	settings(false, true)
	r = cli(harness.RequesterPerson, true, "demo-signin")
	if h := byID(r, "demo-signin"); h.State != harness.StateNeedsAuth || h.Auth.Status != harness.SignedOut {
		t.Fatalf("demo-signin = %+v %+v", h.State, h.Auth)
	}
	if s := statusRuns(); s != 1 {
		t.Fatalf("status commands = %d, want 1", s)
	}
	cliReport := cli(harness.RequesterAgent, false)

	// The daemon's production wiring reads the same cache and reports the
	// same, without probing (auto_probe is off).
	o := productionHarnessOptions(func() []string { return []string{a.Repo.Dir} })
	if o.Detector == nil || o.Store == nil || o.AutoProbe == nil || o.AutoProbe() {
		t.Fatalf("production options %+v", o)
	}
	if d := o.PollInterval(); d != harness.DefaultPollInterval {
		t.Fatalf("poll interval %v", d)
	}
	src := &watch.HarnessSource{Detector: o.Detector, Store: o.Store, Bus: bus.New(bus.Options{}), AutoProbe: o.AutoProbe, PollInterval: o.PollInterval}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx) }()
	deadline := time.Now().Add(10 * time.Second)
	for src.Passes() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	daemonReport, ok := src.Last()
	if !ok {
		t.Fatal("the daemon ran no pass")
	}
	// A console probe runs under the daemon's Detector as a person; within
	// the cooldown it reuses the result and starts nothing.
	console, err := a.Harnesses(personProbeContext(context.Background(), o.Detector), app.HarnessQuery{Probe: true, IDs: []string{"demo-ready"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"demo-ready", "demo-signin", "demo-signin-off"} {
		c, d := byID(cliReport, id), byID(daemonReport, id)
		if c.State != d.State || c.Code != d.Code || c.Version != d.Version || c.Auth.Status != d.Auth.Status ||
			watch.HarnessProbeOutcome(c.Probe) != watch.HarnessProbeOutcome(d.Probe) {
			t.Errorf("%s: CLI %s/%s %s %+v %+v, daemon %s/%s %s %+v %+v", id, c.State, c.Code, c.Version, c.Auth, c.Probe, d.State, d.Code, d.Version, d.Auth, d.Probe)
		}
	}
	if c, d := byID(cliReport, "demo-ready"), byID(console, "demo-ready"); c.State != d.State || c.Code != d.Code {
		t.Errorf("console probe %s/%s, CLI %s/%s", d.State, d.Code, c.State, c.Code)
	}
	if n, s := starts(), statusRuns(); n != 3 || s != 1 {
		t.Fatalf("the daemon and the reads started %d agents and %d status commands, want 3 and 1", n, s)
	}

	// No byte of the status command's output is kept.
	err = filepath.Walk(filepath.Join(home, "harnesses"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), secret) {
			t.Errorf("%s holds the status output", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
