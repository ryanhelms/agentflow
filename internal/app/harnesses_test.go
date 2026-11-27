package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
)

// stubDetector answers every pass with report and records what it was asked.
type stubDetector struct {
	report harness.Report
	err    error
	asked  []harness.DetectOptions
}

func (d *stubDetector) Detect(_ context.Context, o harness.DetectOptions) (harness.Report, error) {
	d.asked = append(d.asked, o)
	return d.report, d.err
}

// useDetector makes d the Detector of the rest of the test and returns an App
// with an empty agentflow home.
func useDetector(t *testing.T, d harness.Detector) *App {
	t.Helper()
	home := harnesstest.PATH(t)
	previous := NewHarnessDetector
	NewHarnessDetector = func(*App) (harness.Detector, error) { return d, nil }
	t.Cleanup(func() { NewHarnessDetector = previous })
	return &App{Home: home}
}

func TestHarnessesPassesTheQueryAndTheRequester(t *testing.T) {
	d := &stubDetector{}
	a := useDetector(t, d)
	report, err := a.Harnesses(t.Context(), HarnessQuery{All: true, IDs: []string{"gemini"}, Probe: true})
	if err != nil {
		t.Fatal(err)
	}
	if report.Harnesses == nil {
		t.Error("harnesses is nil: JSON would say null")
	}
	for _, requester := range []string{harness.RequesterPerson, harness.RequesterDaemon, harness.RequesterAgent, "repository-console", ""} {
		if _, err := a.Harnesses(WithHarnessRequester(t.Context(), requester), HarnessQuery{}); err != nil {
			t.Fatal(err)
		}
	}
	first := d.asked[0]
	if !first.All || !first.Probe || !slices.Equal(first.IDs, []string{"gemini"}) {
		t.Errorf("asked %+v", first)
	}
	var got []string
	for _, o := range d.asked {
		got = append(got, o.Requester)
	}
	// Nothing on the context, and anything but person or daemon, is an agent.
	want := []string{"agent", "person", "daemon", "agent", "agent", "agent"}
	if !slices.Equal(got, want) {
		t.Errorf("requesters %v, want %v", got, want)
	}
}

func TestHarnessesReturnsTheDetectorError(t *testing.T) {
	a := useDetector(t, &stubDetector{err: harness.ErrTestGuard})
	if _, err := a.Harnesses(t.Context(), HarnessQuery{}); !errors.Is(err, harness.ErrTestGuard) {
		t.Errorf("Harnesses: %v", err)
	}
	if _, err := a.HarnessDoctor(t.Context(), HarnessDoctorQuery{}); !errors.Is(err, harness.ErrTestGuard) {
		t.Errorf("HarnessDoctor: %v", err)
	}
}

var doctorNow = time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)

func healthyReport() harness.Report {
	return harness.Report{
		Registry:  harness.RegistryInfo{Commit: "4273056f03b93f8ce41e9276256ea47b057b794c", CommittedAt: doctorNow.Add(-24 * time.Hour), Agents: 46},
		AutoProbe: true,
		Harnesses: []harness.Harness{{ID: "gemini", Name: "Gemini CLI", State: harness.StateNative,
			Probe: harness.Probe{Status: harness.ProbeOK, Code: harness.CodeOK}}},
		CheckedAt: doctorNow,
	}
}

func daemonUp(context.Context) (bool, error) { return true, nil }

func doctorChecks(t *testing.T, a *App, q HarnessDoctorQuery) (map[string]DoctorCheck, DoctorReport) {
	t.Helper()
	r, err := a.HarnessDoctor(t.Context(), q)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	byID := map[string]DoctorCheck{}
	for _, c := range r.Checks {
		ids = append(ids, c.ID)
		byID[c.ID] = c
		if (c.Status == CheckOK) != (c.Fix == nil) {
			t.Errorf("%s: status %s with fix %+v", c.ID, c.Status, c.Fix)
		}
	}
	want := []string{"registry", "home", "path", "definitions", "cache", "daemon", "auto_probe", "sign_in", "harnesses"}
	if !slices.Equal(ids, want) {
		t.Fatalf("checks %v, want %v", ids, want)
	}
	return byID, r
}

func TestHarnessDoctorAllOK(t *testing.T) {
	d := &stubDetector{report: healthyReport()}
	a := useDetector(t, d)
	checks, r := doctorChecks(t, a, HarnessDoctorQuery{Probe: true, DaemonRunning: daemonUp})
	for id, c := range checks {
		if c.Status != CheckOK {
			t.Errorf("%s: %+v", id, c)
		}
	}
	if r.Failed() {
		t.Error("Failed with every check ok")
	}
	if len(d.asked) != 1 || !d.asked[0].Probe || d.asked[0].All || d.asked[0].Requester != "agent" {
		t.Errorf("asked %+v", d.asked)
	}
}

func TestHarnessDoctorFindsEachProblem(t *testing.T) {
	signIn := &harness.Fix{Text: "Sign in to Codex, then probe again.", Command: "codex login"}
	probe := &harness.Fix{Text: "Not checked yet. Probe it to confirm it speaks ACP.", Command: "agentflow ai harnesses --probe"}
	tests := []struct {
		name   string
		change func(*harness.Report)
		check  string
		status string
		says   []string // in the message, the fix text or its command
	}{
		{"old registry", func(r *harness.Report) { r.Registry.CommittedAt = doctorNow.Add(-121 * 24 * time.Hour) },
			"registry", CheckWarn, []string{"4273056f03b9 from", "Update agentflow"}},
		{"registry at the limit", func(r *harness.Report) { r.Registry.CommittedAt = doctorNow.Add(-120 * 24 * time.Hour) },
			"registry", CheckOK, nil},
		{"unsafe home", func(r *harness.Report) {
			r.Problems = []harness.Problem{{Layer: "personal", Code: harness.ProblemHomeUnsafe, Message: "the home is inside a repository"}}
		}, "home", CheckFail, []string{"inside a repository", "owned by you"}},
		{"unsafe home is not a definition", func(r *harness.Report) {
			r.Problems = []harness.Problem{{Layer: "personal", Code: harness.ProblemHomeUnsafe}}
		}, "definitions", CheckOK, nil},
		{"skipped entries", func(r *harness.Report) {
			r.SkippedPath = []harness.SkippedDir{{Dir: "~/app/bin", Reason: harness.SkipInsideRepository},
				{Dir: "/opt/shared", Reason: harness.SkipUnsafePermissions}, {Dir: "/gone", Reason: harness.SkipMissing}}
		}, "path", CheckWarn, []string{"~/app/bin (inside_repository)", "absolute path in your overrides", "/opt/shared is writable by others"}},
		{"only a missing entry", func(r *harness.Report) {
			r.SkippedPath = []harness.SkippedDir{{Dir: "/gone", Reason: harness.SkipMissing}}
		}, "path", CheckOK, nil},
		{"personal definition", func(r *harness.Report) {
			r.Problems = []harness.Problem{{Layer: "personal", ID: "my-agent", Code: "launcher", Message: "command: npx is never run"}}
		}, "definitions", CheckFail, []string{"refused: 1", "npx is never run", "personal:ai.harnesses.overrides.my-agent"}},
		{"plugin definition", func(r *harness.Report) {
			r.Problems = []harness.Problem{{Layer: "plugin:extra", ID: "x", Code: "rule", Message: "args"}}
		}, "definitions", CheckFail, []string{"plugins.extra.enabled"}},
		{"registry value dropped", func(r *harness.Report) {
			r.Problems = []harness.Problem{{Layer: "registry", ID: "x", Code: harness.ProblemRegistry, Message: "url"}}
		}, "definitions", CheckOK, nil},
		{"not probed with probes off", func(r *harness.Report) {
			r.AutoProbe = false
			r.Harnesses = []harness.Harness{{ID: "goose", State: harness.StateNative, Probe: harness.Probe{Status: harness.ProbeNone, Code: harness.CodeNotProbed}, Fix: probe}}
		}, "auto_probe", CheckWarn, []string{"goose", "ai.harnesses.auto_probe", "agentflow ai harnesses --probe"}},
		{"refused with probes off", func(r *harness.Report) {
			r.AutoProbe = false
			r.Harnesses = []harness.Harness{{ID: "goose", State: harness.StateNative, Probe: harness.Probe{Status: harness.ProbeRefused, Code: harness.CodeNeedsPerson}}}
		}, "auto_probe", CheckWarn, []string{"goose"}},
		{"probes off but all probed", func(r *harness.Report) { r.AutoProbe = false }, "auto_probe", CheckOK, nil},
		{"needs sign-in", func(r *harness.Report) {
			r.Harnesses = append(r.Harnesses, harness.Harness{ID: "codex-acp", Name: "Codex", State: harness.StateNeedsAuth, Fix: signIn})
		}, "sign_in", CheckWarn, []string{"Codex (codex-acp)", "codex login"}},
		{"sign-in checks off", func(r *harness.Report) {
			r.Harnesses[0].Auth = harness.SignInStatus{Status: harness.SignInUnknown, Code: harness.CodeOff}
		}, "sign_in", CheckWarn, []string{"cannot tell whether you are signed in"}},
		{"cached result is ready", func(r *harness.Report) {
			r.Harnesses[0].Probe = harness.Probe{Status: harness.ProbeCached, Code: harness.CodeOK}
		}, "harnesses", CheckOK, []string{"Ready: gemini."}},
		{"cached failure is not ready", func(r *harness.Report) {
			r.Harnesses[0].Probe = harness.Probe{Status: harness.ProbeCached, Code: harness.CodeTimeout}
		}, "harnesses", CheckWarn, []string{"agentflow ai harnesses --probe"}},
		{"the first fix", func(r *harness.Report) {
			r.Harnesses = []harness.Harness{{ID: "a", State: harness.StateIncompatible}, {ID: "codex-acp", State: harness.StateNeedsAuth, Fix: signIn}, {ID: "goose", State: harness.StateNative, Fix: probe}}
		}, "harnesses", CheckWarn, []string{"codex login"}},
		{"nothing found", func(r *harness.Report) { r.Harnesses = nil },
			"harnesses", CheckWarn, []string{"No ACP harnesses found", "agentflow ai harnesses --all"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := healthyReport()
			test.change(&report)
			a := useDetector(t, &stubDetector{report: report})
			checks, r := doctorChecks(t, a, HarnessDoctorQuery{DaemonRunning: daemonUp})
			c := checks[test.check]
			if c.Status != test.status {
				t.Fatalf("%s = %+v, want %s", test.check, c, test.status)
			}
			text := c.Message
			if c.Fix != nil {
				text += "\n" + c.Fix.Text + "\n" + c.Fix.Command
			}
			for _, want := range test.says {
				if !strings.Contains(text, want) {
					t.Errorf("%s does not say %q:\n%s", test.check, want, text)
				}
			}
			// A failed check fails the report; a warning alone never does.
			if (test.status == CheckFail && !r.Failed()) || (test.status == CheckWarn && r.Failed()) {
				t.Errorf("Failed = %v with %s %s", r.Failed(), test.check, test.status)
			}
		})
	}
}

func TestHarnessDoctorDaemon(t *testing.T) {
	a := useDetector(t, &stubDetector{report: healthyReport()})
	for name, running := range map[string]func(context.Context) (bool, error){
		"none":    func(context.Context) (bool, error) { return false, nil },
		"unknown": func(context.Context) (bool, error) { return false, errors.New("no profile") },
		"unset":   nil,
	} {
		checks, _ := doctorChecks(t, a, HarnessDoctorQuery{DaemonRunning: running})
		if c := checks["daemon"]; c.Status != CheckWarn || c.Fix.Command != "agentflow ui" {
			t.Errorf("%s: %+v", name, c)
		}
	}
}

func TestHarnessDoctorCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes")
	}
	tests := []struct {
		name    string
		content string
		mode    os.FileMode
		dirMode os.FileMode
		status  string
		says    string
	}{
		{"readable", `{"records":{}}`, 0o600, 0o700, CheckOK, "can be read"},
		{"not JSON", `{"records":`, 0o600, 0o700, CheckWarn, "cannot be read"},
		{"too large", strings.Repeat(" ", harness.DefaultMaxCacheLen) + "{}", 0o600, 0o700, CheckWarn, "cannot be read"},
		{"readable by others", `{}`, 0o644, 0o700, CheckWarn, "wrong mode"},
		{"directory open to others", `{}`, 0o600, 0o755, CheckWarn, "wrong mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := useDetector(t, &stubDetector{report: healthyReport()})
			dir := filepath.Join(a.Home, "harnesses")
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "cache.json")
			if err := os.WriteFile(file, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(file, test.mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, test.dirMode); err != nil {
				t.Fatal(err)
			}
			checks, _ := doctorChecks(t, a, HarnessDoctorQuery{DaemonRunning: daemonUp})
			if c := checks["cache"]; c.Status != test.status || !strings.Contains(c.Message, test.says) {
				t.Errorf("cache = %+v", c)
			}
		})
	}
}
