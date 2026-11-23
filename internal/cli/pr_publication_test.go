package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/nscaledev/devflow/internal/app"
	"github.com/nscaledev/devflow/internal/mcpserver"
	"github.com/nscaledev/devflow/internal/policy"
)

const (
	publicationTitle  = "feat: publish the branch"
	publicationBody   = "Exercise the publication policy."
	publicationOptIn  = "  allow_auto_pr_publication: true\n"
	publicationConfig = "commit: {require_key: false}\ntask_management: {provider: none}\npolicy:\n"
	publicationPolicy = publicationConfig + "  max_autonomy: {remote: confirm}\n"
	publicationUser   = "autonomy: {remote: confirm, actions: {push: auto, pr_open: auto, pr_open_draft: auto}}\n"
)

func publicationGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writePublicationFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

// Git sees a GitHub SSH remote. Its SSH transport serves only this test's bare
// repository, including the default branch and the published feature ref.
func publicationRepository(t *testing.T, basePolicy, branchPolicy, user string) string {
	t.Helper()
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("DEVFLOW_HOME", home)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GH_TOKEN", "fixture")
	original := http.DefaultTransport
	http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected HTTP request %s %s", r.Method, r.URL)
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	writePublicationFile(t, filepath.Join(home, "config.yml"), user)
	bare := t.TempDir()
	publicationGit(t, "init", "--quiet", "--template=", "--bare", "-b", "main", bare)
	script := filepath.Join(t.TempDir(), "ssh.sh")
	writePublicationFile(t, script, `case "$*" in
  *git-receive-pack*) exec git-receive-pack "$DEVFLOW_TEST_BARE" ;;
  *git-upload-pack*) exec git-upload-pack "$DEVFLOW_TEST_BARE" ;;
  *) exit 1 ;;
esac
`)
	t.Setenv("DEVFLOW_TEST_BARE", filepath.ToSlash(bare))
	t.Setenv("DEVFLOW_TEST_SSH", filepath.ToSlash(script))
	t.Setenv("GIT_SSH_COMMAND", `sh "$DEVFLOW_TEST_SSH"`)
	t.Setenv("GIT_SSH_VARIANT", "simple")
	publicationGit(t, "init", "--quiet", "--template=", "-b", "main")
	publicationGit(t, "config", "user.name", "Fixture")
	publicationGit(t, "config", "user.email", "test@example.org")
	publicationGit(t, "remote", "add", "origin", "git@github.com:team/app.git")
	if err := os.Mkdir(".devflow", 0700); err != nil {
		t.Fatal(err)
	}
	writePublicationFile(t, ".devflow/config.yml", basePolicy)
	publicationGit(t, "add", ".devflow/config.yml")
	publicationGit(t, "commit", "--quiet", "-m", "chore: configure repository policy")
	publicationGit(t, "push", "--quiet", "-u", "origin", "main")
	publicationGit(t, "remote", "set-head", "origin", "main")
	publicationGit(t, "switch", "--quiet", "-c", "feature/pr")
	writePublicationFile(t, ".devflow/config.yml", branchPolicy)
	publicationGit(t, "add", ".devflow/config.yml")
	publicationGit(t, "commit", "--quiet", "--allow-empty", "-m", publicationTitle)
	return bare
}

func publicationCLI(t *testing.T, command string, args ...string) app.Pending {
	t.Helper()
	raw, err := captureCLI(t, command, args...)
	var p app.Pending
	if err != nil || json.Unmarshal([]byte(raw), &p) != nil {
		t.Fatalf("%s %v: %s, %v", command, args, raw, err)
	}
	return p
}

func publicationMCP(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	server, err := mcpserver.New(ctx, ".", "test")
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	go func() { _ = server.MCP().Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "pr-publication", Version: "1"}, nil)
	session, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func callPublicationTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, response any) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("%s: %+v, %v", name, result, err)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok || json.Unmarshal([]byte(text.Text), response) != nil {
		t.Fatalf("%s: invalid result %+v", name, result.Content)
	}
}

func TestPRPublicationCLIAndMCP(t *testing.T) {
	for _, tc := range []struct {
		name  string
		draft bool
	}{{"draft", true}, {"ready", false}} {
		t.Run(tc.name, func(t *testing.T) {
			const optedIn = publicationPolicy + publicationOptIn
			bare := publicationRepository(t, optedIn, optedIn, publicationUser)
			sha := publicationGit(t, "rev-parse", "HEAD")
			opened := 0
			const pulls = "https://api.github.com/repos/team/app/pulls"
			original := http.DefaultTransport
			http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
				rec := httptest.NewRecorder()
				switch {
				case r.Method == "POST" && r.URL.String() == pulls:
					var pr struct {
						Title, Body, Head, Base string
						Draft                   bool
					}
					if err := json.NewDecoder(r.Body).Decode(&pr); err != nil {
						return nil, err
					}
					if pr.Title != publicationTitle || pr.Body != publicationBody || pr.Head != "feature/pr" || pr.Base != "main" || pr.Draft != tc.draft {
						return nil, fmt.Errorf("unexpected GitHub PR fields: %+v", pr)
					}
					out, err := exec.CommandContext(r.Context(), "git", "--git-dir="+bare, "rev-parse", "refs/heads/feature/pr").Output()
					if err != nil {
						return nil, fmt.Errorf("read published branch: %w", err)
					}
					if strings.TrimSpace(string(out)) != sha {
						return nil, fmt.Errorf("published branch at PR creation: %s, want %s", out, sha)
					}
					opened++
					fmt.Fprintf(rec, `{"number":7,"html_url":"https://github.com/team/app/pull/7","draft":%v,"head":{"sha":%q}}`, tc.draft, sha)
				case r.Method == "GET" && r.URL.Host+r.URL.Path == "api.github.com/repos/team/app/pulls" && tc.draft && opened == 1:
					fmt.Fprintf(rec, `[{"number":7,"node_id":"PR_7","state":"open","draft":true,"base":{"ref":"main"},"head":{"ref":"feature/pr","sha":%q}}]`, sha)
				case r.Method == "POST" && r.URL.String() == "https://api.github.com/graphql":
					var operation struct{ Query string }
					if err := json.NewDecoder(r.Body).Decode(&operation); err != nil {
						return nil, err
					}
					if !strings.HasPrefix(operation.Query, "query(") || !strings.Contains(operation.Query, "PullRequestReviewThread") {
						return nil, fmt.Errorf("unexpected GraphQL operation %q", operation.Query)
					}
					_, _ = rec.WriteString(`{"data":{"node":{"pullRequest":{"number":7,"repository":{"nameWithOwner":"team/app"}}}}}`)
				default:
					return nil, fmt.Errorf("unexpected GitHub request %s %s", r.Method, r.URL)
				}
				return rec.Result(), nil
			})
			t.Cleanup(func() { http.DefaultTransport = original })
			if got := publicationCLI(t, "push", "--prepare"); got.Status != "needs_confirmation" || got.Policy == nil || got.Policy.Source != "policy.max_autonomy.remote" {
				t.Fatalf("standalone push: %+v", got)
			}
			p := publicationCLI(t, "pr", "--prepare", "--title", publicationTitle, "--body", publicationBody, fmt.Sprintf("--draft=%v", tc.draft))
			if p.Status != "prepared" || p.ConfirmToken == "" || p.Policy == nil || p.Policy.Source != "policy.allow_auto_pr_publication" {
				t.Fatalf("publication preview: %+v", p)
			}
			for _, field := range []string{"--reviewer", "--label"} {
				extended := publicationCLI(t, "pr", "--prepare", "--title", publicationTitle, "--body", publicationBody, fmt.Sprintf("--draft=%v", tc.draft), field, "team")
				if extended.Status != "needs_confirmation" {
					t.Fatalf("publication with %s: %+v", field, extended)
				}
			}
			if _, err := captureCLI(t, "push", "--confirm-token", p.ConfirmToken); !errors.Is(err, policy.ErrChanged) {
				t.Fatalf("standalone push with PR approval: %v", err)
			}
			if refs := publicationGit(t, "--git-dir="+bare, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main" || opened != 0 {
				t.Fatalf("preparation created refs or a PR: %s, %d", refs, opened)
			}

			session := publicationMCP(t)
			var response struct {
				Status string
				Number int
				Draft  bool
			}
			callPublicationTool(t, session, "devflow_pr_open", map[string]any{
				"title": publicationTitle, "body": publicationBody, "draft": tc.draft, "confirm_token": p.ConfirmToken,
			}, &response)
			if response.Status != "opened" || response.Number != 7 || response.Draft != tc.draft || opened != 1 {
				t.Fatalf("unexpected PR result: %+v, %d requests", response, opened)
			}
			var reply app.Pending
			callPublicationTool(t, session, "devflow_review_reply", map[string]any{"thread_id": "T1", "body": "Fixed."}, &reply)
			if reply.Status != "needs_confirmation" {
				t.Fatalf("review reply: %+v", reply)
			}
			if tc.draft {
				if got := publicationCLI(t, "pr", "mark-ready", "--prepare"); got.Status != "needs_confirmation" {
					t.Fatalf("draft promotion: %+v", got)
				}
			}
			publicationGit(t, "switch", "--quiet", "main")
			if _, err := captureCLI(t, "pr", "--prepare", "--title", publicationTitle); err == nil || !strings.Contains(err.Error(), "feature branch") {
				t.Fatalf("protected branch publication: %v", err)
			}
		})
	}
}

func TestPRPublicationRequiresBothOptInsAndActionPermissions(t *testing.T) {
	const (
		enabled    = publicationPolicy + publicationOptIn
		pushCapped = publicationConfig + publicationOptIn + "  max_autonomy: {remote: confirm, actions: {push: confirm}}\n"
		suggest    = publicationConfig + publicationOptIn + "  max_autonomy: {remote: suggest}\n"
	)
	for _, tc := range []struct {
		name, basePolicy, checkoutPolicy, personal, status string
	}{
		{"default", publicationPolicy, publicationPolicy, publicationUser, "needs_confirmation"},
		{"only on branch", publicationPolicy, enabled, publicationUser, "needs_confirmation"},
		{"only on default branch", enabled, publicationPolicy, publicationUser, "needs_confirmation"},
		{"personal push inherits confirm", enabled, enabled, "autonomy: {remote: confirm, actions: {pr_open: auto}}", "needs_confirmation"},
		{"personal PR suggest", enabled, enabled, "autonomy: {remote: auto, actions: {pr_open: suggest}}", "suggest"},
		{"repository push ceiling", enabled, pushCapped, publicationUser, "needs_confirmation"},
		{"repository remote suggest", enabled, suggest, publicationUser, "suggest"},
		{"target push ceiling", pushCapped, enabled, publicationUser, "needs_confirmation"},
		{"target remote suggest", suggest, enabled, publicationUser, "suggest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bare := publicationRepository(t, tc.basePolicy, tc.checkoutPolicy, tc.personal)
			p := publicationCLI(t, "pr", "--prepare", "--title", publicationTitle, "--body", publicationBody)
			if p.Status != tc.status {
				t.Fatalf("publication decision: want %q, got %+v", tc.status, p)
			}
			if refs := publicationGit(t, "--git-dir="+bare, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main" {
				t.Fatalf("publication preview changed remote refs: %s", refs)
			}
		})
	}
}

func TestPRPublicationRechecksTargetBeforeApply(t *testing.T) {
	const enabled = publicationPolicy + publicationOptIn
	bare := publicationRepository(t, enabled, enabled, publicationUser)
	args := []string{"--title", publicationTitle, "--body", publicationBody}
	preview := publicationCLI(t, "pr", append([]string{"--prepare"}, args...)...)
	if preview.Status != "prepared" || preview.ConfirmToken == "" {
		t.Fatalf("initial PR preview: %+v", preview)
	}

	// A separate maintainer disables the policy after the preview. Leave the
	// publishing worktree's origin/main and local object database untouched.
	maintainer := t.TempDir()
	publicationGit(t, "clone", "--quiet", bare, maintainer)
	writePublicationFile(t, filepath.Join(maintainer, ".devflow/config.yml"), publicationPolicy)
	publicationGit(t, "-C", maintainer, "-c", "user.name=Fixture", "-c", "user.email=test@example.org", "commit", "--quiet", "-am", "chore: disable automatic publication")
	publicationGit(t, "-C", maintainer, "push", "--quiet", "origin", "main")

	if _, err := captureCLI(t, "pr", append(args, "--confirm-token", preview.ConfirmToken)...); !errors.Is(err, policy.ErrChanged) {
		t.Fatalf("redeeming a prepared token after target revocation: %v", err)
	}
	check := publicationCLI(t, "pr", append([]string{"--prepare"}, args...)...)
	if check.Status != "needs_confirmation" {
		t.Fatalf("preview against new target revision: %+v", check)
	}
	if refs := publicationGit(t, "--git-dir="+bare, "for-each-ref", "--format=%(refname)"); refs != "refs/heads/main" {
		t.Fatalf("revoked approval changed remote refs: %s", refs)
	}
}
