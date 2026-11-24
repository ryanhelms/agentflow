package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/nscaledev/agentflow/internal/provider/nativeoauth"
)

// OpenResult can safely be returned through chat. The browser alone receives
// the one-time access URL.
type OpenResult struct {
	Destination
	Reused bool `json:"reused"`
}

type startup struct {
	URL    string `json:"url,omitempty"`
	Reused bool   `json:"reused,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Open launches the requested screen on the MCP host's desktop. A new console
// runs independently of the tool call and the MCP transport.
func Open(ctx context.Context, dir string, dest Destination) (OpenResult, error) {
	return openDestination(ctx, dir, dest, false)
}

// OpenAutomatic leaves a live worktree tab on the developer's current screen.
func OpenAutomatic(ctx context.Context, dir string) error {
	_, err := openDestination(ctx, dir, Destination{Screen: "overview"}, true)
	return err
}

func openDestination(ctx context.Context, dir string, dest Destination, automatic bool) (OpenResult, error) {
	dest, err := dest.Normalize("providers")
	if err != nil {
		return OpenResult{}, err
	}
	ready, err := prepareConsole(ctx, dir, dest, tabMode(automatic), 0)
	if err != nil {
		return OpenResult{}, err
	}
	if ready.URL != "" {
		if err := OpenBrowser(ctx, ready.URL); err != nil {
			return OpenResult{}, fmt.Errorf("open the console in a browser: %w. Run %s from this worktree on your desktop", err, dest.command())
		}
	}
	return OpenResult{Destination: dest, Reused: ready.Reused}, nil
}

// LaunchCLI prepares a persistent console and returns its one-time address
// to the local terminal. An empty address means a live tab already navigated.
func LaunchCLI(ctx context.Context, dir string, port int, dest Destination, linkOnly bool) (string, error) {
	mode := "open"
	if linkOnly {
		mode = "link"
	}
	ready, err := prepareConsole(ctx, dir, dest, mode, port)
	return ready.URL, err
}

func prepareConsole(ctx context.Context, dir string, dest Destination, mode string, port int) (startup, error) {
	address, found, err := findConsole(ctx, dir, dest, mode, port)
	if err != nil || found {
		return startup{URL: address, Reused: found}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return startup{}, fmt.Errorf("locate agentflow executable: %w", err)
	}
	cmd := exec.Command(exe, "ui", "--serve-child", dest.Screen, dest.Role, mode, strconv.Itoa(port))
	cmd.Dir = dir
	// Resolve custom relative socket paths in the caller's directory before
	// the daemon starts in the repository selected by the CLI.
	if socket := commanderSocket(); socket != "" {
		cmd.Env = append(os.Environ(), "AGENT_COMMANDER_SOCKET="+socket)
	}
	return startAndWait(ctx, cmd)
}

// ServeChild is the private stdout handshake between two copies of agentflow.
// Closing the pipe after readiness leaves the child with no MCP descriptors.
func ServeChild(ctx context.Context, dest Destination, mode string, port int, pipe *os.File) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	defer pipe.Close()
	sent := false
	err := serveAt(ctx, ".", port, dest, func(address string, reused bool) {
		sent = true
		if err := json.NewEncoder(pipe).Encode(startup{URL: address, Reused: reused}); err != nil {
			stop()
		}
		_ = pipe.Close()
	}, mode)
	if err != nil && !sent {
		_ = json.NewEncoder(pipe).Encode(startup{Error: err.Error()})
	}
	return err
}

func startAndWait(ctx context.Context, cmd *exec.Cmd) (startup, error) {
	if err := ctx.Err(); err != nil {
		return startup{}, err
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return startup{}, fmt.Errorf("create console startup pipe: %w", err)
	}
	defer reader.Close()
	cmd.Stdout = writer
	detach(cmd)
	err = cmd.Start()
	_ = writer.Close()
	if err != nil {
		return startup{}, fmt.Errorf("start console: %w", err)
	}
	go func() { _ = cmd.Wait() }()

	// The child may serve for hours. Read one bounded message, never its
	// process lifetime. The caller owns a child only until it reports ready.
	report := make(chan startup, 1)
	go func() {
		var ready startup
		if err := json.NewDecoder(io.LimitReader(reader, 8192)).Decode(&ready); err != nil {
			ready = startup{Error: "console exited before reporting readiness"}
		}
		report <- ready
	}()
	starting, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	select {
	case <-starting.Done():
		_ = cmd.Process.Kill()
		return startup{}, fmt.Errorf("wait for console startup: %w", starting.Err())
	case ready := <-report:
		if ready.Error != "" {
			return startup{}, errors.New(ready.Error)
		}
		if ready.URL == "" && !ready.Reused {
			return startup{}, errors.New("console did not return a launch address")
		}
		return ready, nil
	}
}

// OpenBrowser dispatches through the operating system. Some desktop helpers
// stay attached to a running browser; keep reaping the helper after dispatch.
func OpenBrowser(ctx context.Context, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- nativeoauth.OpenBrowser(context.WithoutCancel(ctx), address) }()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			// A launch URL contains a credential. Never include browser output
			// or command arguments in an error returned to an MCP client.
			return errors.New("the operating system could not open a desktop browser")
		}
		return nil
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// DaemonStatus reports the currently registered listener without starting it.
func DaemonStatus(ctx context.Context) (bool, int, error) {
	return daemonCommand(ctx, "status")
}

// StopDaemon requests an orderly shutdown without touching any checkout.
func StopDaemon(ctx context.Context) (bool, int, error) {
	return daemonCommand(ctx, "stop")
}
