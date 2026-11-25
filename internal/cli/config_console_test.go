package cli

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/agentflow/internal/app"
	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/consolecfg"
	"github.com/nscaledev/agentflow/internal/mcpserver"
)

func TestConfigConsoleThroughCLIAndMCP(t *testing.T) {
	ctx := t.Context()
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("AGENTFLOW_HOME", home)
	t.Setenv("AGENTFLOW_NO_UPDATE", "1")
	t.Setenv("CODEX_THREAD_ID", "console-cli-owner")
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	var remoteCalls atomic.Int32
	originalTransport := http.DefaultTransport
	http.DefaultTransport = taskTransport(func(*http.Request) (*http.Response, error) {
		remoteCalls.Add(1)
		return nil, errors.New("external requests are disabled in this fixture")
	})
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	for _, args := range [][]string{{"init", "-q", "--template=", "-b", "main"}, {"remote", "add", "origin", "https://github.com/example/console"}, {"-c", "user.name=Fixture", "-c", "user.email=test@example.org", "commit", "-q", "--allow-empty", "-m", "chore: initial commit"}} {
		if out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v, %s", args[0], err, out)
		}
	}
	if err := os.Mkdir(".agentflow", 0700); err != nil {
		t.Fatal(err)
	}
	const source = "agentflow: 1\ncommit: {require_key: false}\ntask_management: {provider: github-issues}\n# repository source marker\n"
	const privateMarker = "private-fixture-preference"
	const personal = "autonomy: {remote: confirm}\n# " + privateMarker + "\n"
	write := func(path, text string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	personalPath := filepath.Join(home, "config.yml")
	write(consolecfg.MainFile, source)
	write(personalPath, personal)

	connect := func(t *testing.T) *mcp.ClientSession {
		t.Helper()
		ctx := t.Context()
		server, err := mcpserver.New(ctx, ".", "config-mcp-worker")
		if err != nil {
			t.Fatal(err)
		}
		st, ct := mcp.NewInMemoryTransports()
		go func() { _ = server.MCP().Run(ctx, st) }()
		client := mcp.NewClient(&mcp.Implementation{Name: "config-client", Version: "1"}, nil)
		session, err := client.Connect(ctx, ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = session.Close() })
		return session
	}
	session := connect(t)
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	annotations := map[string]bool{}
	for _, tool := range listed.Tools {
		if tool.Name == "agentflow_config" || tool.Name == "agentflow_config_apply" {
			annotations[tool.Name] = tool.Annotations != nil && tool.Annotations.ReadOnlyHint
		}
	}
	if len(annotations) != 2 || !annotations["agentflow_config"] || annotations["agentflow_config_apply"] {
		t.Fatalf("configuration tools or read-only hints: %v", annotations)
	}

	call := func(t *testing.T, name string, args map[string]any, wantProblem string, out any) {
		t.Helper()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s protocol: %v", name, err)
		}
		if len(result.Content) == 0 {
			t.Fatalf("%s returned no content", name)
		}
		block, ok := result.Content[0].(*mcp.TextContent)
		if !ok || strings.Contains(block.Text, privateMarker) {
			t.Fatalf("%s exposed private settings or returned unexpected content", name)
		}
		if wantProblem != "" {
			if !result.IsError || !strings.Contains(block.Text, wantProblem) {
				t.Fatalf("%s expected %q: %s", name, wantProblem, block.Text)
			}
			return
		}
		if result.IsError {
			t.Fatalf("%s: %s", name, block.Text)
		}
		if err := json.Unmarshal([]byte(block.Text), out); err != nil {
			t.Fatalf("%s JSON: %v", name, err)
		}
	}
	command := func(t *testing.T, op string, args map[string]any, out any) {
		t.Helper()
		flags := []string{op}
		if args != nil {
			raw, err := json.Marshal(args)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "request.json")
			write(path, string(raw))
			flags = append(flags, "--input", path)
		}
		raw, err := captureCLI(t, "config", flags...)
		if err != nil || strings.Contains(raw, privateMarker) {
			t.Fatalf("config %s failed or exposed private settings: %v", op, err)
		}
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			t.Fatalf("config %s JSON: %v", op, err)
		}
	}
	const query = "agentflow_config"
	const apply = "agentflow_config_apply"

	t.Run("typed source edits and bound publication", func(t *testing.T) {
		var inventory consolecfg.Snapshot
		command(t, "list", nil, &inventory)
		if len(inventory.Files) == 0 || len(inventory.Schemas) == 0 {
			t.Fatal("inventory omitted sources or provider schemas")
		}
		for _, f := range inventory.Files {
			if f.Content != "" {
				t.Fatalf("list returned content for %s", f.Path)
			}
		}
		var f consolecfg.File
		call(t, query, map[string]any{"operation": "read", "path": consolecfg.MainFile}, "", &f)
		if f.Content != source || f.Digest == "" || !f.Editable {
			t.Fatal("read did not return the registered source and digest")
		}
		var composed struct{ Content, Warning string }
		call(t, query, map[string]any{"operation": "compose", "base_digest": f.Digest, "role": "task_management", "setting": "search.limit", "edit": "set", "value": 25}, "", &composed)
		if composed.Warning != "" || !strings.Contains(composed.Content, "limit: 25") {
			t.Fatalf("typed number: %+v", composed)
		}
		var staged struct {
			Content  string
			Autonomy consolecfg.ApprovalView
		}
		call(t, query, map[string]any{"operation": "stage_autonomy", "base_digest": f.Digest, "content": composed.Content, "patch": map[string]any{"remote": "confirm", "allow_auto_pr_publication": true}}, "", &staged)
		if !strings.Contains(staged.Content, "limit: 25") || staged.Autonomy.Repository.Remote != config.Confirm {
			t.Fatal("staging lost the provider edit or repository ceiling")
		}
		if enabled := staged.Autonomy.Repository.AllowAutoPRPublication; enabled == nil || !*enabled {
			t.Fatal("repository PR publication policy missing from MCP preview")
		}
		if staged.Autonomy.Personal.AllowAutoPRPublication != nil {
			t.Fatal("personal autonomy includes repository-only publication policy")
		}
		call(t, query, map[string]any{"operation": "preview_personal", "user_revision": staged.Autonomy.UserRevision, "patch": map[string]any{"allow_auto_pr_publication": false}}, "repository policy setting", nil)
		command(t, "stage_autonomy", map[string]any{"base_digest": f.Digest, "content": staged.Content, "patch": map[string]any{"allow_auto_pr_publication": false}}, &staged)
		if !strings.Contains(staged.Content, "allow_auto_pr_publication: false") || !strings.Contains(staged.Content, "limit: 25") || staged.Autonomy.Repository.Remote != config.Confirm {
			t.Fatal("CLI failed to disable PR publication in the same draft")
		}
		params := map[string]any{"path": f.Path, "base_digest": f.Digest, "content": staged.Content, "title": "chore: adjust task search settings"}
		var preview struct {
			app.Pending
			Draft bool
		}
		command(t, "preview", params, &preview)
		if preview.Status != "needs_confirmation" || preview.ConfirmToken == "" || !preview.Draft {
			t.Fatal("publication omitted default draft mode or required confirmation")
		}
		publish := maps.Clone(params)
		publish["operation"], publish["confirm_token"], publish["mode"] = "publish", preview.ConfirmToken, "direct"
		call(t, apply, publish, "operation_changed", nil)
		delete(publish, "mode")
		write(f.Path, source+"# another change\n")
		call(t, apply, publish, "source changed", nil)
		write(f.Path, source)
		if remoteCalls.Load() != 0 {
			t.Fatal("publication contacted a provider before validating the reviewed change")
		}
	})

	t.Run("CLI owner and MCP worker share one personal save", func(t *testing.T) {
		var before consolecfg.ApprovalView
		command(t, "autonomy", nil, &before)
		if before.UserRevision == "" {
			t.Fatal("autonomy omitted user revision")
		}
		args := map[string]any{"user_revision": before.UserRevision, "patch": map[string]any{"actions": map[string]string{"commit": "auto"}}}
		var owner, worker app.Pending
		command(t, "preview_personal", args, &owner)
		args["operation"] = "preview_personal"
		call(t, query, args, "", &worker)
		if owner.Status != "needs_confirmation" || owner.Approval == nil || owner.ConfirmToken == "" || worker.Status != "awaiting_approval" || worker.Approval == nil || worker.ConfirmToken != "" || worker.Approval.OperationID != owner.Approval.OperationID {
			t.Fatal("MCP did not wait for the exact CLI preview")
		}
		data, err := os.ReadFile(personalPath)
		if err != nil || string(data) != personal {
			t.Fatalf("preview changed personal configuration: %v", err)
		}
		id := owner.Approval.OperationID
		if _, err := captureCLI(t, "policy", "approve", id, "--confirm-token", owner.ConfirmToken); err != nil {
			t.Fatal(err)
		}
		args["operation"], args["confirm_token"] = "save_personal", id
		var saved consolecfg.ApprovalView
		call(t, apply, args, "", &saved)
		if saved.UserRevision == before.UserRevision || saved.Personal.Actions["commit"] != config.Auto || saved.Personal.Remote != config.Confirm {
			t.Fatal("save did not apply just the requested override")
		}
		call(t, apply, args, "changed", nil)
		data, err = os.ReadFile(personalPath)
		if err != nil || !strings.Contains(string(data), privateMarker) {
			t.Fatalf("save lost unrelated personal settings: %v", err)
		}
	})

	t.Run("recover configuration inspection on startup", func(t *testing.T) {
		write(consolecfg.MainFile, source+"unclosed: [\n")
		var invalid consolecfg.Snapshot
		command(t, "list", nil, &invalid)
		if len(invalid.Problems) == 0 || len(invalid.Files) == 0 {
			t.Fatal("could not inspect configuration problems through CLI")
		}
		recovery := connect(t)
		available, err := recovery.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, tool := range available.Tools {
			seen[tool.Name] = true
		}
		if !seen[query] || !seen[apply] || seen["agentflow_commit"] {
			t.Fatal("recovery must expose the config tools and withhold normal write tools")
		}
		response, err := recovery.CallTool(t.Context(), &mcp.CallToolParams{Name: query, Arguments: map[string]any{"operation": "read", "path": consolecfg.MainFile}})
		if err != nil || response.IsError || len(response.Content) == 0 {
			t.Fatalf("recovery read: %v, %v", response, err)
		}
		// The invalid YAML is the human-editable source that needs repair.
		b, ok := response.Content[0].(*mcp.TextContent)
		var file consolecfg.File
		if !ok || json.Unmarshal([]byte(b.Text), &file) != nil || !file.Editable || file.Digest == "" || !strings.Contains(file.Content, "unclosed: [") {
			t.Fatal("MCP recovery omitted the editable source or its digest")
		}
	})
}
