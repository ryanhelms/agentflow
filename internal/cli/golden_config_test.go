package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/consolecfg"
	"github.com/ryanhelms/agentflow/internal/goldentest"
)

// goldenConfigRepo is a repository named app with a GitHub remote, repository
// settings and personal settings. The caller's working directory is the
// repository. Nothing in it can reach a provider.
func goldenConfigRepo(t *testing.T) *goldenCLI {
	t.Helper()
	goldenEnvironment(t, "autonomy: {remote: confirm}\n")
	repo := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	goldenGit(t, repo, "init", "-q", "-b", "main")
	goldenGit(t, repo, "remote", "add", "origin", "https://github.com/example/console")
	goldenGit(t, repo, "-c", "user.name=Dev", "-c", "user.email=dev@example.org", "commit", "-q", "--allow-empty", "-m", "chore: initial commit")
	if err := os.Mkdir(filepath.Join(repo, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	const source = "agentflow: 1\ncommit: {require_key: false}\ntask_management: {provider: github-issues}\n"
	if err := os.WriteFile(filepath.Join(repo, consolecfg.MainFile), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	original := http.DefaultTransport
	http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		return nil, errors.New("external requests are disabled in this fixture")
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	// The provider and action catalogues are pinned by their own tests.
	g := newGoldenCLI(t)
	g.norm.ElideObject("providers").ElideArray("actions")
	return g
}

// field reads one top-level string field from the JSON a command printed.
func field(t *testing.T, r goldentest.Run, name string) string {
	t.Helper()
	var out map[string]any
	if r.Err != nil || json.Unmarshal([]byte(r.Stdout), &out) != nil {
		t.Fatalf("%s: %q, %v", r.Command, r.Stdout, r.Err)
	}
	value, ok := out[name].(string)
	if !ok || value == "" {
		t.Fatalf("%s printed no %s: %q", r.Command, name, r.Stdout)
	}
	return value
}

func jsonText(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGoldenConfigCommand(t *testing.T) {
	t.Run("personal settings", func(t *testing.T) {
		g := goldenConfigRepo(t)
		view := g.run("", "config", "autonomy")
		g.check("config/autonomy.txt", view)
		revision := field(t, view, "user_revision")
		request := map[string]any{"user_revision": revision, "patch": map[string]any{"actions": map[string]string{"commit": "auto"}}}

		preview := g.run(jsonText(t, request), "config", "preview_personal", "--input", "-")
		g.check("config/preview_personal.txt", preview)
		request["confirm_token"] = g.pending(preview).ConfirmToken
		if data, err := os.ReadFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml")); err != nil || strings.Contains(string(data), "commit") {
			t.Fatalf("preview changed the personal settings: %q, %v", data, err)
		}

		saved := g.run(jsonText(t, request), "config", "save_personal", "--input", "-")
		g.check("config/save_personal.txt", saved)
		if data, err := os.ReadFile(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "config.yml")); err != nil || !strings.Contains(string(data), "commit: auto") {
			t.Fatalf("save did not change the personal settings: %q, %v", data, err)
		}
		// The approval is single-use.
		g.check("config/save_personal_again.txt", g.run(jsonText(t, request), "config", "save_personal", "--input", "-"))
	})
	t.Run("repository settings", func(t *testing.T) {
		g := goldenConfigRepo(t)
		read := g.run(`{"path": ".agentflow/config.yml"}`, "config", "read", "--input", "-")
		g.check("config/read.txt", read)
		var file consolecfg.File
		if err := json.Unmarshal([]byte(read.Stdout), &file); err != nil {
			t.Fatal(err)
		}
		request := map[string]any{
			"path": file.Path, "base_digest": file.Digest, "title": "chore: adjust the commit rules",
			"content": strings.Replace(file.Content, "require_key: false", "require_key: false, subject_max: 60", 1),
		}
		preview := g.run(jsonText(t, request), "config", "preview", "--input", "-")
		g.check("config/preview.txt", preview)
		request["confirm_token"] = field(t, preview, "confirm_token")

		// Publishing needs the original token and the same reviewed change.
		request["title"] = "chore: adjust the commit rules again"
		g.check("config/publish_changed_title.txt", g.run(jsonText(t, request), "config", "publish", "--input", "-"))
	})
}
