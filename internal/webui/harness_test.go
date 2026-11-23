package webui

import (
	"strings"
	"testing"
)

func TestClaudeSessionStartEvent(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{
		{`{"hook_event_name":"SessionStart","source":"startup","session_id":"s"}`, true},
		{`{"hook_event_name":"SessionStart","source":"resume","session_id":"s"}`, true},
		{`{"hook_event_name":"SessionStart","source":"clear","session_id":"s"}`, false},
		{`{"hook_event_name":"SessionStart","source":"startup","session_id":"s","agent_id":"worker"}`, false},
		{`{"hook_event_name":"PreToolUse","source":"startup","session_id":"s"}`, false},
		{"", false},
	} {
		if got := claudeSessionStart(strings.NewReader(tc.input)); got != tc.want {
			t.Errorf("hook payload %s: got %t", tc.input, got)
		}
	}
}

func TestInteractiveHarnessEvidence(t *testing.T) {
	for _, tc := range []struct {
		client, executable, command string
		open                        bool
	}{
		{"claude", "/usr/bin/claude", "claude", true},
		{"claude", "/home/me/.local/share/claude/versions/2.1", "claude --resume", true},
		{"claude", "/usr/bin/claude", "claude -p plan", false},
		{"claude", "/usr/bin/claude", "claude mcp list", false},
		{"codex", "/usr/bin/codex", "codex resume", true},
		{"codex", "/usr/bin/codex", "codex exec review", false},
		{"gemini", "/usr/bin/node", "node /opt/gemini.js", true},
		{"gemini", "/usr/bin/node", "node /opt/gemini.js --prompt plan", false},
		{"grok", "/usr/bin/grok", "grok", true},
		{"cursor", "/usr/bin/cursor-agent", "cursor-agent", true},
		{"claude", "/usr/bin/other", "other claude", false},
	} {
		if got := terminalHost(tc.client, tc.executable, tc.command); got != tc.open {
			t.Errorf("terminalHost(%q, %q, %q) = %t", tc.client, tc.executable, tc.command, got)
		}
	}
	for _, tc := range []struct{ client, path string }{
		{"vscode", "/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper"},
		{"cursor", "/Applications/Cursor.app/Contents/Frameworks/Cursor Helper"},
		{"codex", "/Applications/Codex.app/Contents/MacOS/Codex"},
		{"claude", "/Applications/Claude.app/Contents/MacOS/Claude"},
		{"vscode", `C:\Program Files\Microsoft VS Code\Code.exe`},
	} {
		if !desktopHost(tc.client, tc.path) || desktopHost("unknown", tc.path) {
			t.Errorf("desktop host detection for %s", tc.path)
		}
	}
}
