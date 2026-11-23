package webui

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// Claude plugins receive SessionStart for scripted invocations as well as the
// UI. Require an actual top-level startup/resume and a local interactive host.
func OpenClaudeSession(ctx context.Context, input io.Reader) {
	if AutoOpenSuppressed(false) || !claudeSessionStart(input) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if InteractiveHarness(ctx, "claude", true) {
		_ = OpenAutomatic(ctx, ".")
	}
}

func claudeSessionStart(input io.Reader) bool {
	var event struct {
		Name    string `json:"hook_event_name"`
		Source  string `json:"source"`
		Session string `json:"session_id"`
		Agent   string `json:"agent_id"`
	}
	if err := json.NewDecoder(io.LimitReader(input, 32<<10)).Decode(&event); err != nil {
		return false
	}
	return event.Name == "SessionStart" && event.Session != "" && event.Agent == "" && (event.Source == "startup" || event.Source == "resume")
}

// Only a coordinator's explicit UI-only registration may open a console
// from a managed actor process. Its role signals startup intent; the service
// authorizes requests made with the supplied scoped credential.
func AutoOpenSuppressed(commanderCoordinator bool) bool {
	if runtime.GOOS == "linux" && os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return true
	}

	for _, name := range []string{"CI", "CONTINUOUS_INTEGRATION", "GITHUB_ACTIONS", "AGENT_COMMANDER_FOCUSED_PROOF", "CLAUDE_CODE_REMOTE"} {
		switch strings.ToLower(os.Getenv(name)) {
		case "", "0", "false", "no":
		default:
			return true
		}
	}
	for _, name := range []string{"SSH_CONNECTION", "SSH_TTY", "SSH_CLIENT", "CLAUDE_INTERNAL_ASSISTANT_TEAM_NAME"} {
		if os.Getenv(name) != "" {
			return true
		}
	}
	id, token, role := os.Getenv("AGENT_COMMANDER_WORKER_ID"), os.Getenv("AGENT_COMMANDER_TOKEN"), os.Getenv("AGENT_COMMANDER_ROLE")
	if id != "" || token != "" || role != "" {
		return !commanderCoordinator || role != "coordinator" || id == "" || token == "" || !filepath.IsAbs(os.Getenv("AGENT_COMMANDER_SOCKET"))
	}
	return false
}

// Recognize an editor's own app/extension process, or an interactive CLI
// launched from a terminal. A terminal or agent merely running inside some
// other editor is not evidence that the named harness is interactive.
func desktopHost(client, exe string) bool {
	exe = strings.ToLower(strings.ReplaceAll(exe, "\\", "/"))
	name := filepath.Base(exe)
	switch client {
	case "claude", "claude-desktop":
		return (strings.Contains(exe, "/claude.app/contents/") && strings.Contains(exe, "/contents/macos/")) || name == "claude desktop.exe" || (name == "claude.exe" && strings.Contains(exe, "/appdata/local/anthropicclaude/"))
	case "codex":
		return strings.Contains(exe, "/codex.app/contents/macos/") || (name == "codex.exe" && strings.Contains(exe, "/windowsapps/openai.codex"))
	case "cursor":
		return strings.Contains(exe, "/cursor.app/contents/") || name == "cursor" || name == "cursor.exe"
	case "vscode":
		return strings.Contains(exe, "/visual studio code.app/contents/") || strings.Contains(exe, "/visual studio code - insiders.app/contents/") || slices.Contains([]string{"code", "code.exe", "code-insiders", "code - insiders.exe"}, name)
	}
	return false
}

func terminalHost(client, executable, command string) bool {
	executable = strings.ToLower(executable)
	name := filepath.Base(executable)
	if !slices.Contains([]string{"claude", "codex", "gemini", "grok", "cursor"}, client) {
		return false
	}
	args := strings.Fields(command)
	if len(args) == 0 {
		return false
	}
	// Match installed Node entry points as well as standalone executables.
	if name == "node" && len(args) > 1 {
		executable = strings.ToLower(args[1])
		name = strings.TrimSuffix(filepath.Base(executable), ".js")
		args = args[1:]
	}
	if client == "claude" && strings.Contains(executable, "/claude/versions/") {
		name = "claude"
	}
	if client == "cursor" {
		client = "cursor-agent"
	}
	if name != client {
		return false
	}
	// Only unambiguous interactive invocations qualify; e.g. exec, -p,
	// orchestration agents and mcp list are all deliberately silent.
	args = args[1:]
	return len(args) == 0 || (len(args) == 1 && slices.Contains([]string{"resume", "--resume", "--continue", "-c"}, args[0]))
}
