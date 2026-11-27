package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/webui"
)

const fakeUILink = "http://127.0.0.1:41511/w/x/#launch=TICKETSECRET.proof&screen=overview"

// clearAgentEnv removes every variable app.AgentEnvironment reads, so the
// test decides who the caller is.
func clearAgentEnv(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, value, _ := strings.Cut(kv, "=")
		switch {
		case name == "CLAUDECODE", name == "CLAUDE_CODE_SESSION_ID", name == "AGENTFLOW_CODEX_HOST_ACTIVE", name == app.SessionEnv,
			strings.HasPrefix(name, "CODEX_") && name != "CODEX_HOME":
			t.Setenv(name, value)
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if name := app.AgentEnvironment(); name != "" {
		t.Fatalf("agent environment still set: %s", name)
	}
}

type uiFakes struct {
	launched, opened int
	openErr          error
}

func fakeUI(t *testing.T, terminal bool) *uiFakes {
	t.Helper()
	f := &uiFakes{}
	oldTerm, oldLaunch, oldOpen, oldServeOpen, oldServeLink := ops.StdinIsTerminal, uiLaunch, uiOpenBrowser, uiServeOpen, uiServeLink
	t.Cleanup(func() {
		ops.StdinIsTerminal, uiLaunch, uiOpenBrowser, uiServeOpen, uiServeLink = oldTerm, oldLaunch, oldOpen, oldServeOpen, oldServeLink
	})
	ops.StdinIsTerminal = func() bool { return terminal }
	uiLaunch = func(context.Context, string, int, webui.Destination, bool) (string, error) {
		f.launched++
		return fakeUILink, nil
	}
	serve := func(_ context.Context, _ string, _ int, _ webui.Destination, ready func(string, bool)) error {
		f.launched++
		ready(fakeUILink, false)
		return nil
	}
	uiServeOpen, uiServeLink = serve, serve
	uiOpenBrowser = func(_ context.Context, address string) error {
		f.opened++
		if address != fakeUILink {
			t.Errorf("browser got %q", address)
		}
		return f.openErr
	}
	return f
}

// captureUI runs agentflow ui with args and returns stdout, stderr and the
// error text.
func captureUI(t *testing.T, args ...string) (stdout, stderr, errText string) {
	t.Helper()
	outF, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errF
	runErr := runUI(t.Context(), args)
	os.Stdout, os.Stderr = oldOut, oldErr
	read := func(f *os.File) string {
		_, _ = f.Seek(0, 0)
		b, _ := io.ReadAll(f)
		_ = f.Close()
		return string(b)
	}
	if runErr != nil {
		errText = runErr.Error()
	}
	return read(outF), read(errF), errText
}

func TestUILinkOnlyForAPersonAtATerminal(t *testing.T) {
	flagSets := [][]string{
		nil,
		{"--no-open"},
		{"--foreground"},
		{"--foreground", "--no-open"},
		{"--screen", "ai"},
		{"--screen", "ai", "--no-open"},
	}
	callers := []struct {
		name     string
		agentEnv string
		terminal bool
	}{
		{"agent environment", "CLAUDECODE", true},
		{"codex environment", "CODEX_SESSION", true},
		{"AI session", app.SessionEnv, true},
		{"non-terminal stdin", "", false},
	}
	for _, c := range callers {
		for _, args := range flagSets {
			for _, openErr := range []error{nil, errors.New("no browser at " + fakeUILink)} {
				name := c.name + " " + strings.Join(args, " ")
				if openErr != nil {
					name += " browser fails"
				}
				t.Run(name, func(t *testing.T) {
					clearAgentEnv(t)
					if c.agentEnv != "" {
						t.Setenv(c.agentEnv, "1")
					}
					f := fakeUI(t, c.terminal)
					f.openErr = openErr
					stdout, stderr, errText := captureUI(t, args...)
					for what, s := range map[string]string{"stdout": stdout, "stderr": stderr, "error": errText} {
						if strings.Contains(s, "TICKETSECRET") || strings.Contains(s, "launch=") {
							t.Errorf("%s holds the link: %q", what, s)
						}
					}
					noOpen := strings.Contains(strings.Join(args, " "), "--no-open")
					switch {
					case noOpen:
						if f.launched != 0 || f.opened != 0 || !strings.Contains(errText, "own terminal") {
							t.Errorf("--no-open: launched %d, opened %d, error %q", f.launched, f.opened, errText)
						}
					case openErr != nil:
						if f.opened != 1 || !strings.Contains(stderr, "own terminal") || errText != "" {
							t.Errorf("failed open: opened %d, stderr %q, error %q", f.opened, stderr, errText)
						}
					default:
						if f.opened != 1 || !strings.Contains(stdout, "Opened the console in your browser") || errText != "" {
							t.Errorf("open: opened %d, stdout %q, error %q", f.opened, stdout, errText)
						}
					}
				})
			}
		}
	}
}

func TestUILinkPrintedForAPerson(t *testing.T) {
	for _, args := range [][]string{nil, {"--no-open"}, {"--foreground"}, {"--foreground", "--no-open"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			clearAgentEnv(t)
			f := fakeUI(t, true)
			stdout, _, errText := captureUI(t, args...)
			if errText != "" || !strings.Contains(stdout, fakeUILink) {
				t.Fatalf("person: stdout %q, error %q", stdout, errText)
			}
			wantOpened := 1
			if len(args) > 0 && args[len(args)-1] == "--no-open" {
				wantOpened = 0
			}
			if f.opened != wantOpened {
				t.Errorf("opened %d, want %d", f.opened, wantOpened)
			}
		})
	}
}

func TestLinkFreeErrors(t *testing.T) {
	err := errors.New("control failed for " + fakeUILink)
	if got := linkFree(err, false); strings.Contains(got.Error(), "launch=") {
		t.Errorf("agent error %q", got)
	}
	if got := linkFree(err, true); got != err {
		t.Errorf("person error changed: %v", got)
	}
}
