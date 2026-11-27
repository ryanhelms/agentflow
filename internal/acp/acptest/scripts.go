package acptest

import (
	"encoding/json"

	"github.com/ryanhelms/agentflow/internal/acp"
)

// The scripts of cmd/fake-acp's session modes, which the phase 7 demo uses
// (ADR "Phase 7 demo and gate"). Each is a fresh value.

// SessionModeScripts maps a cmd/fake-acp --mode value to its script.
var SessionModeScripts = map[string]func() *SessionScript{
	"session-demo":   SessionDemo,
	"session-nested": SessionNested,
	"session-hang":   SessionHang,
	"session-auth":   SessionAuth,
}

func update(v any) Step { return Step{Update: JSON(v)} }

func chunk(text string) Step {
	return update(map[string]any{
		"sessionUpdate": acp.UpdateAgentMessageChunk,
		"content":       map[string]any{"type": "text", "text": text},
	})
}

func askPermission(id, title string, kind acp.ToolKind, path string) Step {
	return Step{Ask: &Ask{Permission: JSON(map[string]any{
		"toolCall": map[string]any{
			"toolCallId": id, "title": title, "kind": kind, "status": acp.ToolPending,
			"locations": []map[string]any{{"path": path}},
		},
		"options": []map[string]any{
			{"optionId": "allow-once", "name": "Allow once", "kind": acp.AllowOnce},
			{"optionId": "allow-always", "name": "Always allow", "kind": acp.AllowAlways},
			{"optionId": "reject-once", "name": "Reject", "kind": acp.RejectOnce},
			{"optionId": "reject-always", "name": "Always reject", "kind": acp.RejectAlways},
		},
	})}}
}

func askRead(path string) Step {
	return Step{Ask: &Ask{Read: JSON(map[string]any{"path": path})}}
}

func askWrite(path, content string) Step {
	return Step{Ask: &Ask{Write: JSON(map[string]any{"path": path, "content": content})}}
}

func askTerminal(command string, args ...string) Step {
	return Step{Ask: &Ask{Terminal: JSON(map[string]any{"command": command, "args": args, "cwd": "{cwd}"})}}
}

// waitAndRelease waits for and releases the last terminal, if one was
// created.
func waitAndRelease() []Step {
	return []Step{
		{Ask: &Ask{Wait: json.RawMessage(`{}`)}},
		{Ask: &Ask{Release: json.RawMessage(`{}`)}},
	}
}

// SessionDemo is --mode=session-demo: message chunks; a plan; a read of
// notes.txt in the worktree; a permission request for an edit of
// src/hello.txt and the write itself; a read of /etc/hosts; a write of
// .claude/settings.json; terminal/create "echo hi" (waited for and
// released when it starts); an execute tool call with no request; a usage
// update with a cost; end_turn.
func SessionDemo() *SessionScript {
	steps := []Step{
		chunk("I will add a greeting. "),
		chunk("First I read the notes."),
		update(map[string]any{
			"sessionUpdate": acp.UpdatePlan,
			"entries": []map[string]any{
				{"content": "Read notes.txt", "priority": "high", "status": "in_progress"},
				{"content": "Write src/hello.txt", "priority": "medium", "status": "pending"},
			},
		}),
		askRead("{cwd}/notes.txt"),
		askPermission("edit-hello", "Write src/hello.txt", acp.ToolEdit, "{cwd}/src/hello.txt"),
		askWrite("{cwd}/src/hello.txt", "hello, world\n"),
		askRead("/etc/hosts"),
		askWrite("{cwd}/.claude/settings.json", "{}\n"),
		askTerminal("echo", "hi"),
	}
	steps = append(steps, waitAndRelease()...)
	steps = append(steps,
		Step{ToolUnasked: JSON(map[string]any{
			"toolCallId": "run-tests", "title": "Run the tests", "kind": acp.ToolExecute,
			"rawInput": map[string]any{"command": "make test"},
		})},
		update(map[string]any{
			"sessionUpdate": acp.UpdateUsage, "used": 1200, "size": 200000,
			"cost": map[string]any{"amount": 0.01, "currency": "USD"},
		}),
		chunk("Done."),
		Step{Stop: acp.StopEndTurn},
	)
	return &SessionScript{Steps: steps}
}

// SessionNested is --mode=session-nested: terminal requests that a session
// must never be allowed: starting a session, allowing an ask, and reading
// the session's answer.key (spelled with $AGENTFLOW_HOME, which the
// session never-list expands); then end_turn.
func SessionNested() *SessionScript {
	return &SessionScript{Steps: []Step{
		chunk("Starting another session."),
		askTerminal("agentflow", "ai", "run", "--harness", "demo", "x"),
		askTerminal("agentflow", "ai", "respond", "{session}", "ask-1", "--allow"),
		askTerminal("cat", "$AGENTFLOW_HOME/ai/sessions/{session}/answer.key"),
		{Stop: acp.StopEndTurn},
	}}
}

// SessionHang is --mode=session-hang: a message, a permission request that
// waits for a person, then nothing until session/cancel.
func SessionHang() *SessionScript {
	return &SessionScript{Steps: []Step{
		chunk("Working slowly."),
		askPermission("edit-slow", "Write src/slow.txt", acp.ToolEdit, "{cwd}/src/slow.txt"),
		{Sleep: "24h"},
		{Stop: acp.StopEndTurn},
	}}
}

// SessionAuth is --mode=session-auth: session/new fails with -32000
// (authentication required).
func SessionAuth() *SessionScript {
	return &SessionScript{Steps: []Step{{NewSessionError: acp.CodeAuthRequired}}}
}
