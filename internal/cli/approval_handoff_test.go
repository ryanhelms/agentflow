package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/mcpserver"
	"github.com/nscaledev/devflow/internal/policy"
)

// Exercise both public clients against one real Git index and one approval
// directory. The MCP server remains running while independent CLI invocations
// prepare and grant the operation.
func TestCommitApprovalHandoffCLIAndMCP(t *testing.T) {
	ctx := t.Context()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("DEVFLOW_HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "cli-owner")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(args ...string) string {
		t.Helper()
		data, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, data)
		}
		return strings.TrimSpace(string(data))
	}
	git("init", "--quiet", "--template=", "-b", "feature/approval")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "test@example.org")
	git("commit", "--quiet", "--allow-empty", "-m", "chore: initialize fixture")
	head := git("rev-parse", "HEAD")
	if err := os.Mkdir(".devflow", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".devflow/config.yml", []byte("commit: {require_key: false}\ntask_management: {provider: none}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stage := func(s string) {
		t.Helper()
		if err := os.WriteFile("note.txt", []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "--", "note.txt")
	}
	const initial = "approved first contents\n"
	stage(initial)

	server, err := mcpserver.New(ctx, dir, "mcp-worker")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = server.MCP().Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "approval-fixture", Version: "1"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	call := func(name string, args map[string]any, wantError bool, out any) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Content) != 1 {
			t.Fatalf("%s returned %d content blocks", name, len(result.Content))
		}
		block, ok := result.Content[0].(*mcp.TextContent)
		if !ok || result.IsError != wantError {
			t.Fatalf("%s returned unexpected content or error status: %+v", name, result)
		}
		if err = json.Unmarshal([]byte(block.Text), out); err != nil {
			t.Fatalf("%s JSON: %v", name, err)
		}
	}
	const msg = "fix: record the reviewed note"
	raw, err := captureCLI(t, "commit", "--prepare", "-m", msg)
	var owner app.Pending
	if err != nil || json.Unmarshal([]byte(raw), &owner) != nil || owner.Status != "needs_confirmation" || owner.ConfirmToken == "" || owner.Approval == nil {
		t.Fatal("CLI did not issue a commit preview", err)
	}
	id := owner.Approval.OperationID
	var waiting app.Pending
	call("devflow_commit_prepare", map[string]any{"message": msg}, false, &waiting)
	if waiting.Status != "awaiting_approval" || waiting.ConfirmToken != "" || waiting.Approval == nil || waiting.Approval.OperationID != id {
		t.Fatal("MCP worker did not wait on the same operation")
	}
	var state policy.Approval
	call("devflow_approval_status", map[string]any{"operation_id": id}, false, &state)
	if state.State != "pending" || state.PromptOwner != owner.Approval.PromptOwner {
		t.Fatalf("unexpected shared status: %+v", state)
	}
	raw, err = captureCLI(t, "policy", "approve", id, "--confirm-token", owner.ConfirmToken)
	if err != nil || strings.Contains(raw, owner.ConfirmToken) || json.Unmarshal([]byte(raw), &state) != nil || state.State != "approved" || git("rev-parse", "HEAD") != head {
		t.Fatal("CLI grant must be bearer-free and must not change HEAD", err)
	}
	args := map[string]any{"message": msg, "confirm_token": id}
	var problem struct{ Code string }
	stage("different content under the same filename\n")
	call("devflow_commit", args, true, &problem)
	if problem.Code != "operation_changed" || git("rev-parse", "HEAD") != head {
		t.Fatalf("accepted changed index or unclear failure: %s", problem.Code)
	}
	stage(initial)
	var committed app.CommitResult
	call("devflow_commit", args, false, &committed)
	if committed.Status != "committed" || git("rev-parse", committed.SHA) != git("rev-parse", "HEAD") || git("show", "HEAD:note.txt") != strings.TrimSpace(initial) || git("rev-list", "--count", head+"..HEAD") != "1" {
		t.Fatalf("unexpected committed contents: %+v", committed)
	}
	call("devflow_approval_status", map[string]any{"operation_id": id}, false, &state)
	if state.State != "consumed" {
		t.Fatal("approval was not consumed", state)
	}
	stage("subsequent change\n")
	call("devflow_commit", args, true, &problem)
	if problem.Code != "approval_consumed" || git("rev-parse", "HEAD") != git("rev-parse", committed.SHA) {
		t.Fatal("approval replay changed HEAD or returned the wrong reason", problem.Code)
	}

	for _, tc := range []struct{ name, answer string }{{"decline", "n\n"}, {"eof", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			input, err := os.CreateTemp(t.TempDir(), "answer")
			if err != nil {
				t.Fatal(err)
			}
			defer input.Close()
			if _, err = input.WriteString(tc.answer); err != nil {
				t.Fatal(err)
			}
			if _, err = input.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			stdin := os.Stdin
			os.Stdin = input
			defer func() { os.Stdin = stdin }()
			msg := "fix: handle " + tc.name
			raw, err := captureCLI(t, "commit", "--prepare", "-m", msg, "--path", "note.txt")
			var shown app.Pending
			if err != nil || json.Unmarshal([]byte(raw), &shown) != nil || shown.Approval == nil {
				t.Fatal("missing preview", err)
			}
			_, err = captureCLI(t, "commit", "-m", msg, "--path", "note.txt")
			if err == nil || !strings.Contains(err.Error(), "cancelled") {
				t.Fatal("expected a declined CLI confirmation", err)
			}
			var cancelled policy.Approval
			call("devflow_approval_status", map[string]any{"operation_id": shown.Approval.OperationID}, false, &cancelled)
			if cancelled.State != "cancelled" {
				t.Fatalf("abandoned owner kept the prompt after %s: %+v", tc.name, cancelled)
			}
			var next app.Pending
			call("devflow_commit_prepare", map[string]any{"message": msg, "paths": []string{"note.txt"}}, false, &next)
			if next.Status != "needs_confirmation" || next.Approval == nil || next.Approval.OperationID == shown.Approval.OperationID {
				t.Fatalf("waiting worker could not take a fresh prompt after %s", tc.name)
			}
		})
	}
}
