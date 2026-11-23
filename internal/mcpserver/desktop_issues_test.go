//go:build darwin || linux

package mcpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/managedgh"
	"github.com/nscaledev/devflow/internal/setup"
)

func TestDesktopCreateIssueWithManagedGH(t *testing.T) {
	ctx := t.Context()
	dir, home, fixture := gitRepo(t), t.TempDir(), t.TempDir()
	t.Setenv("DEVFLOW_HOME", home)
	t.Setenv("GH_FIXTURE", fixture)
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITHUB_ACTIONS", "GITHUB_RUN_ID", "DEVFLOW_OPERATION_ID"} {
		t.Setenv(name, "")
	}
	put := func(path, value string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(filepath.Join(dir, ".devflow", "config.yml"), "task_management: {provider: github-issues, settings: {repository: sample/project}}\n")
	bin := t.TempDir()
	for _, name := range []string{"git", "bash"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin) // Desktop can run git, but gh is available only as a verified pin.
	source, err := filepath.Abs("testdata/gh-issues.sh")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	pin := fmt.Sprintf("gh:\n- platform: {os: %s, arch: %s}\n  version: 2.0.0\n  source: %s\n  sha256: %x\n",
		runtime.GOOS, runtime.GOARCH, (&url.URL{Scheme: "file", Path: source}).String(), sha256.Sum256(raw))
	put(filepath.Join(home, "companions.yml"), pin)
	selection, err := managedgh.Inspect(ctx, dir)
	if err != nil || selection.Install == nil {
		t.Fatalf("fixture pin: %v, %v", selection, err)
	}
	if err := selection.Install.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	plan, err := setup.PlanProviderSetup(ctx, dir, "claude-desktop")
	if err != nil {
		t.Fatal(err)
	}
	foundCLI := false
	for _, tool := range plan.Tooling {
		if tool.Provider == "github-issues" && tool.CLI != nil {
			foundCLI = tool.CLI.Installed
		}
	}
	if !foundCLI {
		t.Fatal("provider tooling omitted the installed GitHub CLI")
	}
	s, err := New(ctx, dir, "claude") // The Desktop project launcher uses this client name.
	if err != nil {
		t.Fatal(err)
	}
	server, client := mcp.NewInMemoryTransports()
	go func() { _ = s.MCP().Run(ctx, server) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, client, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	read := func(args map[string]any) string {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "devflow_context", Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("issue context: %v, %v", res, err)
		}
		return res.Content[0].(*mcp.TextContent).Text
	}
	readArgs := map[string]any{"key": "sample/project#17"}
	var request nativeReadResponse
	if err := json.Unmarshal([]byte(read(readArgs)), &request); err != nil {
		t.Fatal(err)
	}
	if request.Request == nil || request.Status != "native_read_required" || request.Server != "github" ||
		request.Tool != "issue_read" || request.ID == "" {
		t.Fatalf("native issue read: %+v", request.Request)
	}
	if offer := request.SetupOffer; offer == nil || offer.Name != "github" ||
		offer.Endpoint != "https://api.githubcopilot.com/mcp/" || offer.Guide == "" ||
		!strings.Contains(offer.PromptIfMissing, "connect github MCP") {
		t.Fatalf("GitHub connector offer: %+v", offer)
	}
	const description = "Line one\nLine \"two\""
	args := map[string]any{"summary": "Example", "description": description}
	create := func(wantError bool) string {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "devflow_ticket_create", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		body := res.Content[0].(*mcp.TextContent).Text
		if res.IsError != wantError {
			t.Fatalf("create (want error %v): %s", wantError, body)
		}
		return body
	}
	if got := create(true); !strings.Contains(got, "authentication required") {
		t.Fatal(got)
	}
	if err := setup.LoginProvider(ctx, filepath.Join(dir, ".devflow"), "github-issues", "", "", nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	readArgs["native_results"] = []map[string]string{{"id": request.ID, "status": "unavailable"}}
	if got := read(readArgs); !strings.Contains(got, "Example issue from gh") {
		t.Fatalf("GitHub issue fallback: %s", got)
	}
	var preview struct {
		Status  string
		Token   string `json:"confirm_token"`
		Summary struct {
			Backend string
			Account struct {
				ID    int64
				Login string
			} `json:"github_account"`
		}
	}
	if err := json.Unmarshal([]byte(create(false)), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.Status != "needs_confirmation" || preview.Token == "" || preview.Summary.Backend != "gh" || preview.Summary.Account.ID != 42 || preview.Summary.Account.Login != "fixture-user" {
		t.Fatalf("preview: %+v", preview)
	}
	args["confirm_token"] = preview.Token
	actor := filepath.Join(fixture, "actor")
	put(actor, `{"id":43,"login":"fixture-user"}`)
	if got := create(true); !strings.Contains(got, "request changed") {
		t.Fatal(got)
	}
	put(actor, `{"id":42,"login":"fixture-user"}`)
	args["summary"] = "Different"
	if got := create(true); !strings.Contains(got, "request changed") {
		t.Fatal(got)
	}
	posts := filepath.Join(fixture, "posts")
	if _, err := os.Stat(posts); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected POST before approval: %v", err)
	}
	args["summary"] = "Example"
	if got := create(false); !strings.Contains(got, "sample/project#17") {
		t.Fatal(got)
	}
	if got := create(true); !strings.Contains(got, "already used") {
		t.Fatal(got)
	}
	ledger, err := os.ReadFile(posts)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct{ Title, Body string }
	if bytes.Count(ledger, []byte("\n")) != 1 || json.Unmarshal(ledger, &payload) != nil || payload.Title != "Example" ||
		!strings.HasSuffix(payload.Body, "\n\n"+description) {
		t.Fatalf("unexpected fixture requests: %s", ledger)
	}
}
