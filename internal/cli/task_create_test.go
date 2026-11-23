package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/mcpserver"
	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/filesystem"
)

type taskTransport func(*http.Request) (*http.Response, error)

func (f taskTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func taskCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return captureCLI(t, "task", append([]string{"create"}, args...)...)
}

func captureCLI(t *testing.T, command string, args ...string) (string, error) {
	t.Helper()
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	stdout := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = stdout }()
	err = run(t.Context(), command, args)
	if _, seekErr := output.Seek(0, 0); seekErr != nil {
		t.Fatal(seekErr)
	}
	b, readErr := io.ReadAll(output)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(b), err
}

func TestTaskCreationCLIAndMCP(t *testing.T) {
	for _, name := range []string{"asana", "filesystem"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("DEVFLOW_HOME", t.TempDir())
			t.Setenv("DEVFLOW_OPERATION_ID", "")
			t.Setenv("ASANA_ACCESS_TOKEN", "fixture")
			if out, err := exec.CommandContext(ctx, "git", "init", "--quiet", "--template=", "-b", "main").CombinedOutput(); err != nil {
				t.Fatalf("git init: %s, %v", out, err)
			}
			if err := os.Mkdir(".devflow", 0700); err != nil {
				t.Fatal(err)
			}
			settings := "project: '10'"
			if name == "filesystem" {
				settings = "scope: '10', root: tasks"
			}
			cfg := "task_management:\n  provider: " + name + "\n  settings: {" + settings + "}\n  templates: {catalog: catalog.json, default: {name: plan, revision: '01'}}\n"
			if err := os.WriteFile(".devflow/config.yml", []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			const catalog = `[{"name":"plan","revision":"01","defaults":{"title":"Default title"},"required":["title"]},{"name":"plan","revision":"2","defaults":{"title":"Selected title"},"required":["title"],"sections":[{"heading":"Scope","body":"Fixture"}],"checklist":["Verify"]},{"name":"required","revision":"1","required":["description"]}]`
			if err := os.WriteFile("catalog.json", []byte(catalog), 0600); err != nil {
				t.Fatal(err)
			}
			// Capture the requests inside the process; the fake transport has no network fallback.
			var payloads []string
			oldTransport := http.DefaultTransport
			http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
				if name != "asana" || r.Method != "POST" || r.URL.String() != "https://app.asana.com/api/1.0/tasks?opt_fields=gid%2Cpermalink_url" {
					return nil, errors.New("unexpected provider request")
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					return nil, err
				}
				payloads = append(payloads, string(body))
				rec := httptest.NewRecorder()
				_, _ = rec.WriteString(`{"data":{"gid":"100","permalink_url":"https://example.invalid/100"}}`)
				return rec.Result(), nil
			})
			defer func() { http.DefaultTransport = oldTransport }()

			if preview, err := taskCLI(t, "--dry-run"); err != nil || !strings.Contains(preview, "Default title") {
				t.Fatalf("default preview %s, %v", preview, err)
			}
			args := []string{"--template", "plan", "--template-revision", "2", "--summary", "Override"}
			preview, err := taskCLI(t, append(args, "--dry-run")...)
			if err != nil || !strings.Contains(preview, "Override") || !strings.Contains(preview, "Verify") {
				t.Fatalf("preview %s, %v", preview, err)
			}
			if _, err := taskCLI(t, "--template", "plan", "--yes"); err == nil {
				t.Fatal("accepted partial pin")
			}
			if _, err := taskCLI(t, "--template", "required", "--template-revision", "1", "--yes"); err == nil {
				t.Fatal("accepted missing field")
			}
			// Even a piped affirmative answer cannot authorize headless creation.
			pipe, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_, _ = writer.WriteString("yes\n")
			writer.Close()
			oldStdin := os.Stdin
			os.Stdin = pipe
			_, err = taskCLI(t, args...)
			os.Stdin = oldStdin
			pipe.Close()
			if err == nil || !strings.Contains(err.Error(), "--yes") {
				t.Fatalf("headless approval: %v", err)
			}
			if err := os.WriteFile(filepath.Join(os.Getenv("DEVFLOW_HOME"), "config.yml"), []byte("autonomy: {local: suggest, remote: suggest}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := taskCLI(t, append(args, "--yes")...); err == nil || !strings.Contains(err.Error(), "suggest") {
				t.Fatalf("suggest policy: %v", err)
			}
			if len(payloads) != 0 {
				t.Fatal("unexpected write before approval")
			}
			if _, err := os.Stat("tasks"); !os.IsNotExist(err) {
				t.Fatalf("task store before approval: %v", err)
			}
			if err := os.Remove(filepath.Join(os.Getenv("DEVFLOW_HOME"), "config.yml")); err != nil {
				t.Fatal(err)
			}

			s, err := mcpserver.New(ctx, dir, "test")
			if err != nil {
				t.Fatal(err)
			}
			st, ct := mcp.NewInMemoryTransports()
			go func() { _ = s.MCP().Run(ctx, st) }()
			session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			fields := map[string]any{"template": "plan", "template_revision": "2", "summary": "Override"}
			call := func() string {
				t.Helper()
				out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "devflow_ticket_create", Arguments: fields})
				if err != nil {
					t.Fatal(err)
				}
				body := out.Content[0].(*mcp.TextContent).Text
				if out.IsError {
					t.Fatal(body)
				}
				return body
			}
			var cliSummary any
			var pending app.Pending
			if err := json.Unmarshal([]byte(preview), &cliSummary); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(call()), &pending); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cliSummary, pending.Summary) || pending.Status != "needs_confirmation" {
				t.Fatalf("MCP %+v differs from CLI preview %s", pending, preview)
			}
			fields["confirm_token"] = pending.ConfirmToken
			mcpResult := call()
			cliResult, err := taskCLI(t, append(args, "--yes")...)
			if err != nil {
				t.Fatal(err)
			}
			for _, result := range []string{mcpResult, cliResult} {
				var created map[string]string
				if err := json.Unmarshal([]byte(result), &created); err != nil {
					t.Fatal(err)
				}
				ref, err := provider.ParseRef(created["key"])
				if err != nil || ref.Instance != name || ref.Scope != "10" {
					t.Fatalf("result %s, %v", result, err)
				}
				if name == "filesystem" {
					store, err := filesystem.NewTasks(name, "tasks")
					if err != nil {
						t.Fatal(err)
					}
					task, err := store.Get(ctx, ref)
					store.Close()
					if err != nil || task.Title != "Override" || task.Template == nil || task.Template.Revision != "2" || task.Description != "## Scope\nFixture\n\n- [ ] Verify" {
						t.Fatalf("stored %+v, %v", task, err)
					}
				}
			}
			if name == "asana" && (len(payloads) != 2 || payloads[0] != payloads[1] || !strings.Contains(payloads[0], "Override") || !strings.Contains(payloads[0], "Verify")) {
				t.Fatalf("Asana payloads %v", payloads)
			}
		})
	}
}
