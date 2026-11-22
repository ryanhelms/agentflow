package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nscaledev/devflow/internal/mcpserver"
	"github.com/nscaledev/devflow/internal/setup"
)

// Start the real CLI dispatcher and an MCP client in the same isolated
// workspace. Nothing in this fixture depends on an authenticated desktop.
func TestSetupPlanAndApplyAcrossClients(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the bundled VS Code adapter launches through sh")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("DEVFLOW_HOME", filepath.Join(t.TempDir(), "devflow"))
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	bin := t.TempDir()
	for _, program := range []string{"git", "sh"} {
		path, err := exec.LookPath(program)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, program)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin) // no gh, code, cursor, or credentials needed
	if b, err := exec.Command("git", "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, b)
	}
	write := func(name, data string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), mode); err != nil {
			t.Fatal(err)
		}
	}
	const cfg = `code_hosting: {provider: none}
task_management: {provider: filesystem, settings: {root: .devflow/tasks}}
knowledge_base: {provider: none}
devops:
  checks: {provider: none}
  deployments: {provider: none}
  releases: {provider: none}
secops: {provider: none}
notifications: {provider: none}
`
	write(".devflow/config.yml", cfg, 0o644)
	write(".devflow/tasks/fixture.md", "# Fixture", 0o644)
	// Executable sentinels would fail if either readiness path launched them.
	write(".devflow/bin/devflow", "#!/bin/sh\nexit 91\n", 0o755)
	capture := func(wantExit int, args ...string) setup.ProviderSetupPlan {
		t.Helper()
		f, err := os.CreateTemp(t.TempDir(), "stdout")
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		stdout := os.Stdout
		os.Stdout = f
		defer func() { os.Stdout = stdout }()
		if exit := Main(args); exit != wantExit {
			t.Fatalf("devflow %v: exit %d, want %d", args, exit, wantExit)
		}
		data, err := os.ReadFile(f.Name())
		var plan setup.ProviderSetupPlan
		if err != nil || json.Unmarshal(data, &plan) != nil {
			t.Fatalf("invalid plan: %s, %v", data, err)
		}
		return plan
	}
	blocked := capture(1, "doctor", "--providers", "--client", "vscode", "--json")
	if blocked.Ready || !bytes.Contains(mustJSON(t, blocked), []byte("gh attestation verify")) {
		t.Fatalf("missing bootstrap: %+v", blocked)
	}
	write(filepath.Join(os.Getenv("XDG_CACHE_HOME"), "devflow/current/devflow"), "#!/bin/sh\nexit 92\n", 0o755)
	cliPlan := capture(0, "setup", "plan", "--client", "vscode", "--json")
	if cliPlan.Registration == nil || cliPlan.Registration.State != "pending" {
		t.Fatalf("desktop selection: %+v", cliPlan)
	}
	if _, err := os.Stat(os.Getenv("DEVFLOW_HOME")); !os.IsNotExist(err) {
		t.Fatal("preview created developer state")
	}

	ctx := t.Context()
	server, err := mcpserver.New(ctx, dir, "vscode")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = server.MCP().Run(ctx, st) }()
	c := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	session, err := c.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any, result any) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		out := res.Content[0].(*mcp.TextContent).Text
		if res.IsError || json.Unmarshal([]byte(out), result) != nil {
			t.Fatalf("%s: %s", name, out)
		}
	}
	input := map[string]any{"client": "vscode"}
	var mcpPlan setup.ProviderSetupPlan
	call("devflow_setup_plan", input, &mcpPlan)
	if !reflect.DeepEqual(cliPlan, mcpPlan) {
		t.Fatalf("terminal and MCP plans differ: %+v and %+v", cliPlan, mcpPlan)
	}
	// A newly missing provider dependency blocks registration, even if the
	// client previously saw a plan with all provider requirements satisfied.
	write(".devflow/config.yml", strings.Replace(cfg, "filesystem, settings: {root: .devflow/tasks}", "github-issues, settings: {repository: sample/project}", 1), 0o644)
	call("devflow_setup_apply", input, &mcpPlan)
	if mcpPlan.Ready || !bytes.Contains(mustJSON(t, mcpPlan), []byte("install gh on PATH")) {
		t.Fatalf("missing gh did not block apply: %+v", mcpPlan)
	}
	write(".devflow/config.yml", cfg, 0o644)
	var pending struct {
		Status string `json:"status"`
		Token  string `json:"confirm_token"`
	}
	call("devflow_setup_apply", input, &pending)
	if pending.Status != "needs_confirmation" || pending.Token == "" {
		t.Fatalf("missing confirmation: %+v", pending)
	}
	if _, err := os.Stat(".vscode"); !os.IsNotExist(err) {
		t.Fatal("preview modified project registration")
	}
	input["confirm_token"] = pending.Token
	call("devflow_setup_apply", input, &mcpPlan)
	if plan := capture(0, "setup", "verify", "--json", "--client", "vscode"); !plan.Ready || !reflect.DeepEqual(plan, mcpPlan) {
		t.Fatalf("registration not verified: %+v, MCP %+v", plan, mcpPlan)
	}
	capture(0, "setup", "apply", "--json", "--client", "vscode", "--yes")
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
