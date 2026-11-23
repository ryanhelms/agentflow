package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/mcpserver"
	"github.com/nscaledev/devflow/internal/provider"
)

// A reviewer checks out the feature branch without the author's local binding.
// Both clients must resolve the commit trailer to the same issue and documents.
func TestIssueKnowledgeReviewParity(t *testing.T) {
	for _, name := range []string{"repository", "filesystem"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("DEVFLOW_HOME", t.TempDir())
			t.Setenv("DEVFLOW_NO_UPDATE", "1")
			t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
			t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
			t.Setenv("GIT_ALLOW_PROTOCOL", "file")
			t.Setenv("GH_TOKEN", "fixture")

			git := func(args ...string) {
				t.Helper()
				if out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			git("init", "-q", "-b", "main")
			git("config", "user.email", "review@example.invalid")
			git("config", "user.name", "Reviewer")
			for _, sub := range []string{".devflow", "records"} {
				if err := os.Mkdir(sub, 0700); err != nil {
					t.Fatal(err)
				}
			}
			settings := "root: records"
			if name == "repository" {
				settings = fmt.Sprintf("repository: %q, branch: main, directory: records", dir)
			}
			cfg := fmt.Sprintf("task_management: {provider: github-issues}\nknowledge_base: {provider: %s, settings: {%s}}\n", name, settings)
			if err := os.WriteFile(".devflow/config.yml", []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			// Two documents share a title. The first also has a namesake in another scope.
			for i, parts := range [][2]string{{"eng", "one"}, {"eng", "two"}, {"other", "one"}} {
				body := fmt.Sprintf("---\nversion: 1\nkind: document\ninstance: %s\nscope: %s\nid: %s\ntitle: Capacity\n---\nDecision %d.\n", name, parts[0], parts[1], i)
				if err := os.WriteFile(filepath.Join("records", fmt.Sprintf("renamed-%d.md", i)), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", ".")
			git("commit", "-qm", "docs: add decisions")
			git("switch", "-qc", "review")
			git("commit", "-q", "--allow-empty", "-m", "feat: implement decisions", "-m", "Task-Ref: github-issues/acme%2Fapp/7")

			one, two := name+"/eng/one", name+"/eng/two"
			const legacy = "See https://example.invalid/design\n\n## Acceptance Criteria\n- [ ] Verify quota"
			body := legacy + "\n\n## Related Knowledge\n- " + two + "\n- `" + one + "`\n- " + two +
				"\n- confluence/ENG/7\n- " + name + "/wrong/one\n- " + name + "/eng/missing\n- [handbook](https://example.invalid/book)"
			oldTransport := http.DefaultTransport
			defer func() { http.DefaultTransport = oldTransport }()
			http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" || r.URL.String() != "https://api.github.com/repos/acme/app/issues/7" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL)
					return nil, fmt.Errorf("unexpected fixture request")
				}
				w := httptest.NewRecorder()
				err := json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": "open", "title": "Implement quota", "body": body})
				return w.Result(), err
			})

			ctx := t.Context()
			s, err := mcpserver.New(ctx, dir, "test")
			if err != nil {
				t.Fatal(err)
			}
			st, ct := mcp.NewInMemoryTransports()
			go func() { _ = s.MCP().Run(ctx, st) }()
			session, err := mcp.NewClient(&mcp.Implementation{Name: "review", Version: "0"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			call := func(tool string, args map[string]any) string {
				t.Helper()
				r, err := session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
				if err != nil || r.IsError {
					t.Fatalf("%s: %+v, %v", tool, r, err)
				}
				return r.Content[0].(*mcp.TextContent).Text
			}

			out, err := captureCLI(t, "context")
			if err != nil || call("devflow_context", nil) != out {
				t.Fatalf("context differs: %q, %v", out, err)
			}
			wantDocs := "Doc: Capacity " + two + "\nDoc: Capacity " + one + "\n"
			if !strings.Contains(out, wantDocs) || strings.Count(out, "Doc: ") != 2 || strings.Count(out, "! ") != 4 {
				t.Fatalf("resolved documents or warnings: %s", out)
			}
			for _, warning := range []string{"confluence/ENG/7", name + "/wrong/one not found", name + "/eng/missing not found", "expected a qualified"} {
				if !strings.Contains(out, warning) {
					t.Errorf("missing warning for %q: %s", warning, out)
				}
			}

			// Inspect the full decision using the qualified identity in the context.
			ref := provider.Ref{Instance: name, Scope: "eng", ID: "two"}
			out, err = captureCLI(t, "docs", "get", two, "--json")
			if err != nil || strings.TrimSpace(out) != call("devflow_docs_get", map[string]any{"ref": ref}) {
				t.Fatalf("lookup differs: %q, %v", out, err)
			}
			var doc provider.Document
			if err := json.Unmarshal([]byte(out), &doc); err != nil || doc.Ref != ref || doc.Body != "Decision 1.\n" || doc.Revision == "" {
				t.Fatalf("wrong document: %q, %v", out, err)
			}
		})
	}
}
