package claudehost

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Run against a specific installed CLI. No user message or model call is needed.
func TestClaudeControlPlane(t *testing.T) {
	bin := os.Getenv("DEVFLOW_RESEARCH_CLAUDE")
	if bin == "" {
		t.Skip("set DEVFLOW_RESEARCH_CLAUDE to the absolute Claude Code executable")
	}
	if !filepath.IsAbs(bin) {
		t.Fatal("expected an absolute executable path")
	}
	dir := t.TempDir()
	ledger := filepath.Join(dir, "calls.jsonl")
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"probe": map[string]any{"command": fixtureBinary(t), "args": []string{"fixture-provider", ledger}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--bare", "-p", "--verbose", "--no-chrome",
		"--strict-mcp-config", "--mcp-config", string(config), "--setting-sources", "",
		"--input-format", "stream-json", "--output-format", "stream-json",
		"--no-session-persistence", "--tools", "")
	cmd.Dir = dir
	cmd.Env = []string{"HOME=" + dir, "CLAUDE_CONFIG_DIR=" + filepath.Join(dir, "config"),
		"TMPDIR=" + dir, "PATH=/usr/bin:/bin", "ANTHROPIC_API_KEY=fixture-only",
		"ANTHROPIC_BASE_URL=http://127.0.0.1:1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"ENABLE_CLAUDEAI_MCP_SERVERS=false", "DISABLE_AUTOUPDATER=1"}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = in.Close()
			cancel()
			_ = cmd.Wait()
		}
	}()
	enc, dec := json.NewEncoder(in), json.NewDecoder(out)
	type reply struct {
		ID        string          `json:"request_id"`
		Subtype   string          `json:"subtype"`
		Body      json.RawMessage `json:"response"`
		ErrorCode string          `json:"error_code"`
	}
	count := 0
	request := func(req map[string]any) reply {
		t.Helper()
		count++
		id := strconv.Itoa(count)
		if err := enc.Encode(map[string]any{"type": "control_request", "request_id": id, "request": req}); err != nil {
			t.Fatal(err)
		}
		for {
			var event struct {
				Type     string `json:"type"`
				Response reply  `json:"response"`
			}
			if err := dec.Decode(&event); err != nil {
				t.Fatalf("read control response %s: %v", id, err)
			}
			if event.Type == "control_response" && event.Response.ID == id {
				return event.Response
			}
		}
	}
	if res := request(map[string]any{"subtype": "initialize"}); res.Subtype != "success" {
		t.Fatalf("initialize: %+v", res)
	}
	for {
		r := request(map[string]any{"subtype": "mcp_status"})
		var status struct {
			Servers []struct {
				Name, Status string
				Tools        []struct{ Name string }
			} `json:"mcpServers"`
		}
		if err := json.Unmarshal(r.Body, &status); err != nil || len(status.Servers) != 1 {
			t.Fatalf("expected only fixture server: %s, %v", r.Body, err)
		}
		server := status.Servers[0]
		if server.Name != "probe" {
			t.Fatalf("unexpected server %q", server.Name)
		}
		if server.Status == "connected" && len(server.Tools) == 1 && server.Tools[0].Name == "record" {
			break
		}
		if server.Status == "failed" || ctx.Err() != nil {
			t.Fatalf("fixture failed to connect: %s", r.Body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Compare an actual, supported round trip to the reverse MCP envelope.
	resource := request(map[string]any{"subtype": "mcp_read_resource", "serverName": "probe", "uri": resourceURI})
	var read struct{ Contents []struct{ URI, Text string } }
	if err := json.Unmarshal(resource.Body, &read); err != nil || resource.Subtype != "success" ||
		len(read.Contents) != 1 || read.Contents[0].URI != resourceURI || read.Contents[0].Text == "" {
		t.Fatalf("expected a fixture resource containing the process marker: %+v, %v", resource, err)
	}
	// A transport acknowledgement alone cannot verify a provider write.
	attempt := request(map[string]any{"subtype": "mcp_message", "server_name": "probe", "message": map[string]any{
		"jsonrpc": "2.0", "id": 100, "method": "tools/call",
		"params": map[string]any{"name": "record", "arguments": arguments{Text: "fixture", IDs: []int{7}}}}})
	if attempt.Subtype != "success" || len(attempt.Body) != 0 {
		t.Fatalf("reverse routing behavior changed: %+v", attempt)
	}
	candidate := request(map[string]any{"subtype": "mcp_call_tool", "serverName": "probe", "toolName": "record", "arguments": map[string]any{}})
	if candidate.Subtype != "error" || candidate.ErrorCode != "unsupported_request" {
		t.Fatalf("named control request behavior changed: %+v", candidate)
	}
	// Wait for the CLI to close its transports before checking the provider ledger.
	if err := in.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, out); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	seen, err := os.ReadFile(ledger)
	if err != nil || len(seen) != 0 {
		t.Fatalf("expected initialized fixture with zero calls: %q, %v", seen, err)
	}
}
