package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nscaledev/devflow/internal/app"
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

	// Setup writes still require explicit consent when local autonomy is auto.
	write(filepath.Join(os.Getenv("DEVFLOW_HOME"), "config.yml"), "autonomy: {local: auto}\n", 0o600)
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
	githubConfig := strings.Replace(cfg, "filesystem, settings: {root: .devflow/tasks}", "github-issues, settings: {repository: sample/project}", 1)
	write(".devflow/config.yml", githubConfig, 0o644)
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
	t.Run("Claude Code Desktop", func(t *testing.T) {
		// Marketplace enrollment alone does not configure the project server.
		capture(0, "setup", "apply", "--client", "claude", "--yes", "--json")
		marketplace, err := os.ReadFile(".claude/settings.json")
		if err != nil {
			t.Fatal(err)
		}
		preview := capture(1, "doctor", "--providers", "--client", "claude-desktop", "--json")
		args := map[string]any{"client": "claude-desktop"}
		var plan setup.ProviderSetupPlan
		call("devflow_setup_plan", args, &plan)
		if !reflect.DeepEqual(preview, plan) || plan.Registration == nil || plan.Registration.State != "pending" || plan.Registration.Path != ".mcp.json" {
			t.Fatalf("unexpected desktop plan: %+v", plan)
		}
		var consent app.Pending
		call("devflow_setup_apply", args, &consent)
		if consent.ConfirmToken == "" {
			t.Fatal("desktop registration requires confirmation")
		}
		args["confirm_token"] = consent.ConfirmToken
		call("devflow_setup_apply", args, &plan)
		verified := capture(0, "setup", "verify", "--client", "claude-desktop", "--json")
		if !plan.Ready || !reflect.DeepEqual(verified, plan) {
			t.Fatalf("desktop registration not verified: %+v", plan)
		}
		capture(0, "setup", "apply", "--client", "claude-desktop", "--yes", "--json")
		capture(0, "doctor", "--providers", "--client", "claude-desktop", "--json")
		if after, err := os.ReadFile(".claude/settings.json"); err != nil || !bytes.Equal(after, marketplace) {
			t.Fatal("desktop registration changed marketplace enrollment")
		}
	})
	t.Run("managed gh", func(t *testing.T) {
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			t.Skip("local installer requires macOS or Linux")
		}
		home := os.Getenv("DEVFLOW_HOME")
		log := filepath.Join(t.TempDir(), "calls")
		t.Setenv("GH_FIXTURE_LOG", log)
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		source := filepath.Join(t.TempDir(), "raw gh")
		const raw = `#!/bin/sh
printf '%s %s\n' "$0" "$*" >> "$GH_FIXTURE_LOG"
case "$*" in
  "--version"|"auth status --hostname github.com"|"api --hostname github.com --method GET --silent repos/sample/project/issues?per_page=1") ;;
  "auth token") printf 'fixture-token\n' ;;
  *) exit 7 ;;
esac
`
		digest := fmt.Sprintf("%x", sha256.Sum256([]byte(raw)))
		pin := fmt.Sprintf("gh:\n  - platform: {os: %s, arch: %s}\n    version: 2.3.4\n    source: %s\n    sha256: %s\n", runtime.GOOS, runtime.GOARCH, (&url.URL{Scheme: "file", Path: source}).String(), digest)
		manifest := filepath.Join(home, "companions.yml")
		write(manifest, pin, 0o600)
		if p := capture(0, "setup", "plan", "--json"); p.InstallGH != nil {
			t.Fatal("offered gh for filesystem provider")
		}
		write(".devflow/config.yml", githubConfig, 0o644)
		plan := capture(0, "setup", "plan", "--json")
		if plan.Ready || plan.InstallGH == nil || plan.InstallGH.Artifact.SHA256 != digest {
			t.Fatalf("unexpected offer: %+v", plan)
		}
		// Registration-only approval cannot fetch or select the companion.
		capture(1, "setup", "apply", "--client", "cursor", "--yes", "--json")
		args := map[string]any{"install_gh": true}
		call("devflow_setup_apply", args, &pending)
		if pending.Status != "needs_confirmation" || pending.Token == "" {
			t.Fatalf("missing installation confirmation: %+v", pending)
		}
		if _, err := os.Stat(filepath.Join(home, "companions")); !os.IsNotExist(err) {
			t.Fatal("preview wrote the cache")
		}
		args["confirm_token"] = pending.Token
		write(manifest, strings.Replace(pin, "2.3.4", "2.3.5", 1), 0o600)
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "devflow_setup_apply", Arguments: args})
		if err != nil || !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "confirmation token") {
			t.Fatalf("changed pin accepted: %+v, %v", res, err)
		}
		write(manifest, pin, 0o600)
		write(source, raw, 0o600)
		var installed setup.ProviderSetupPlan
		call("devflow_setup_apply", args, &installed)
		if !installed.Ready || installed.InstallGH != nil {
			t.Fatalf("installed gh failed readiness: %+v", installed)
		}
		if err := os.Remove(source); err != nil {
			t.Fatal(err)
		}
		capture(0, "setup", "apply", "--install-gh", "--yes", "--json")
		a, err := app.Open(ctx, dir, "fixture")
		if err != nil {
			t.Fatal(err)
		}
		client, err := a.GitHub(ctx)
		if err != nil || client.Token != "fixture-token" {
			t.Fatalf("provider authentication failed: %v", err)
		}
		calls, err := os.ReadFile(log)
		if err != nil {
			t.Fatal(err)
		}
		executable := filepath.Join(plan.InstallGH.Home, "companions", runtime.GOOS+"-"+runtime.GOARCH, "gh", "2.3.4-"+digest)
		for _, command := range []string{"--version", "auth status --hostname github.com", "api --hostname github.com --method GET", "auth token"} {
			if !strings.Contains(string(calls), executable+" "+command) {
				t.Fatalf("missing %q in %s", command, calls)
			}
		}
		if os.Getenv("PATH") != bin {
			t.Fatal("setup changed PATH")
		}
		if _, err := os.Stat(".cursor"); !os.IsNotExist(err) {
			t.Fatal("installation registered a client")
		}
		// Exercise the CLI write path with a new pin and a broken unmanaged gh.
		write(filepath.Join(bin, "gh"), "#!/bin/sh\nexit 8\n", 0o755)
		write(source, raw, 0o600)
		write(manifest, strings.Replace(pin, "2.3.4", "2.3.5", 1), 0o600)
		capture(0, "setup", "apply", "--install-gh", "--yes", "--json")
		data, err := os.ReadFile(filepath.Join(bin, "gh"))
		if err != nil || string(data) != "#!/bin/sh\nexit 8\n" {
			t.Fatal("unmanaged gh was replaced")
		}
		write(filepath.Join(bin, "gh"), raw, 0o755)
		write(manifest, "malformed: [", 0o600)
		capture(0, "setup", "verify", "--json")
		calls, err = os.ReadFile(log)
		if err != nil || !strings.Contains(string(calls), filepath.Join(bin, "gh")+" auth status") {
			t.Fatal("working PATH installation lost priority")
		}
	})
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
