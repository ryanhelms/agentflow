package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nscaledev/agentflow/internal/config"
)

func TestConsoleDestination(t *testing.T) {
	for _, d := range []string{"overview", "providers", "autonomy", "configuration", "handoff"} {
		got, err := (Destination{Screen: d}).Normalize("overview")
		if err != nil || got.Screen != d || got.Fragment() != "screen="+d {
			t.Fatalf("%s: %+v, %v", d, got, err)
		}
	}
	for _, defaultScreen := range []string{"overview", "providers"} {
		got, err := (Destination{}).Normalize(defaultScreen)
		if err != nil || got.Screen != defaultScreen {
			t.Fatalf("missing screen: %+v, %v", got, err)
		}
	}
	for _, role := range config.ProviderRoles {
		got, err := (Destination{Role: role}).Normalize("overview")
		if err != nil || got.Screen != "providers" || got.Role != role {
			t.Fatalf("role-only: %+v, %v", got, err)
		}
		values, err := url.ParseQuery(got.Fragment())
		if err != nil || values.Get("screen") != "providers" || values.Get("role") != role {
			t.Fatalf("role fragment: %v, %v", values, err)
		}
	}
	for _, invalid := range []Destination{{Screen: "https://example.com"}, {Screen: "overview", Role: "devops.checks"}, {Role: "unsupported"}} {
		if _, err := invalid.Normalize("providers"); err == nil {
			t.Errorf("accepted invalid destination: %+v", invalid)
		}
	}
}

func TestConsoleStartupPipe(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wantErr string
		reused  bool
	}{
		{name: "started"},
		{name: "concurrent", reused: true},
		{name: "failure", wantErr: "unable to listen"},
		{name: "invalid", wantErr: "console exited before reporting readiness"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(executable, "-test.run=^TestConsoleStartupChild$")
			cmd.Env = append(os.Environ(), "AGENTFLOW_TEST_UI_HANDSHAKE="+tc.name)
			ready, err := startAndWait(t.Context(), cmd)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || strings.Contains(err.Error(), "sample-launch") {
					t.Fatalf("startup error: %v", err)
				}
				return
			}
			if err != nil || ready.URL == "" || ready.Reused != tc.reused {
				t.Fatalf("startup returned reused=%v, urlPresent=%v, err=%v", ready.Reused, ready.URL != "", err)
			}
		})
	}
}

func TestConsoleStartupChild(t *testing.T) {
	mode := os.Getenv("AGENTFLOW_TEST_UI_HANDSHAKE")
	if mode == "" {
		return
	}
	switch mode {
	case "failure":
		_ = json.NewEncoder(os.Stdout).Encode(startup{Error: "unable to listen"})
	case "invalid":
		_, _ = os.Stdout.WriteString("unstructured output including sample-launch\n")
	default:
		_ = json.NewEncoder(os.Stdout).Encode(startup{URL: "sample-launch", Reused: mode == "concurrent"})
	}
	// Deliberately keep the child alive beyond the report. The parent should
	// only wait for the message, not process exit or EOF.
	time.Sleep(200 * time.Millisecond)
	os.Exit(0)
}

func TestCanceledConsoleCallDoesNotStartProcesses(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := startAndWait(ctx, exec.Command("unavailable-desktop-tool")); !errors.Is(err, context.Canceled) {
		t.Errorf("start after cancellation: %v", err)
	}
	if err := OpenBrowser(ctx, "unused"); !errors.Is(err, context.Canceled) {
		t.Errorf("open after cancellation: %v", err)
	}
}

func TestDetachedConsoleAfterLauncherExit(t *testing.T) {
	registry := os.Getenv("AGENTFLOW_TEST_DETACHED_REGISTRY")
	op := registration(os.Getenv("AGENTFLOW_TEST_DETACHED_WORKTREE"), "link", "overview")
	makeChild := func(mode string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestDetachedConsoleAfterLauncherExit$")
		cmd.Env = append(os.Environ(), "AGENTFLOW_TEST_DETACHED_MODE="+mode)
		return cmd
	}
	switch os.Getenv("AGENTFLOW_TEST_DETACHED_MODE") {
	case "daemon":
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		err := serveRegistered(ctx, registry, -1, op, func(address string, reused bool) {
			_ = json.NewEncoder(os.Stdout).Encode(startup{URL: address, Reused: reused})
			_ = os.Stdout.Close()
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	case "launcher":
		if _, err := startAndWait(t.Context(), makeChild("daemon")); err != nil {
			t.Fatal(err)
		}
		return
	}
	registry = filepath.Join(t.TempDir(), "registry")
	root := testServer(t).repo.Dir
	t.Setenv("AGENTFLOW_TEST_DETACHED_REGISTRY", registry)
	t.Setenv("AGENTFLOW_TEST_DETACHED_WORKTREE", root)
	if output, err := makeChild("launcher").CombinedOutput(); err != nil {
		t.Fatalf("launcher: %v: %s", err, output)
	}
	prefix, err := daemonPrefix(registry)
	if err != nil {
		t.Fatal(err)
	}
	active, lock, err := discover(t.Context(), prefix)
	if lock != nil {
		_ = lock.Close()
	}
	if err != nil || active == nil {
		t.Fatalf("daemon exited with its launcher: %v", err)
	}
	defer func() { _, _ = active.control(t.Context(), daemonRequest{Action: "stop"}) }()
	op = registration(root, "link", "providers")
	if address, reused, err := findRegistered(t.Context(), registry, op, active.Port); err != nil || !reused || address == "" {
		t.Fatalf("cannot request a fresh link from detached daemon: %v", err)
	}
	if _, _, err := findRegistered(t.Context(), registry, op, active.Port%65535+1); err == nil {
		t.Fatal("ignored conflicting requested port")
	}
}
