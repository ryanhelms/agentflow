package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/goldentest"
)

// goldenPublicationRepo is a repository on branch feature/pr with a GitHub
// remote that is a local bare repository. The caller's working directory is
// the repository.
func goldenPublicationRepo(t *testing.T, user string) *goldenCLI {
	t.Helper()
	goldentest.Isolate(t)
	t.Setenv("HOME", t.TempDir())
	publicationRepository(t, publicationPolicy, publicationPolicy, user)
	return newGoldenCLI(t)
}

func TestGoldenCommitCommand(t *testing.T) {
	const (
		msg  = "feat: add widget"
		user = "autonomy: {local: confirm, remote: confirm}\n"
	)
	fixture := func(t *testing.T, user string) *goldenCLI {
		t.Helper()
		g := goldenPublicationRepo(t, user)
		writePublicationFile(t, "widget.txt", "widget\n")
		return g
	}
	t.Run("prepare", func(t *testing.T) {
		g := fixture(t, user)
		r := g.run("", "commit", "-m", msg, "--path", "widget.txt", "--prepare")
		g.check("commit/prepare.txt", r)
		if got := publicationGit(t, "log", "-1", "--format=%s"); got != publicationTitle {
			t.Fatalf("prepare committed: HEAD is %q", got)
		}
	})
	t.Run("selected path", func(t *testing.T) {
		g := fixture(t, user)
		g.check("commit/path_yes.txt", g.run("", "commit", "-m", msg, "--path", "widget.txt", "--yes"))
		if got := publicationGit(t, "log", "-1", "--format=%s"); got != msg {
			t.Fatalf("HEAD is %q, want %q", got, msg)
		}
	})
	t.Run("staged changes", func(t *testing.T) {
		g := fixture(t, user)
		publicationGit(t, "add", "widget.txt")
		g.check("commit/staged_yes.txt", g.run("", "commit", "-m", msg, "--yes"))
		if got := publicationGit(t, "log", "-1", "--format=%s"); got != msg {
			t.Fatalf("HEAD is %q, want %q", got, msg)
		}
	})
	t.Run("declined", func(t *testing.T) {
		g := fixture(t, user)
		publicationGit(t, "add", "widget.txt")
		g.check("commit/declined.txt", g.run("n\n", "commit", "-m", msg))
		if got := publicationGit(t, "log", "-1", "--format=%s"); got != publicationTitle {
			t.Fatalf("a declined commit changed HEAD to %q", got)
		}
	})
	t.Run("suggest", func(t *testing.T) {
		g := fixture(t, "autonomy: {local: suggest}\n")
		g.check("commit/suggest.txt", g.run("", "commit", "-m", msg, "--path", "widget.txt"))
	})
	t.Run("invalid message", func(t *testing.T) {
		g := fixture(t, user)
		publicationGit(t, "add", "widget.txt")
		g.check("commit/invalid_message.txt", g.run("", "commit", "-m", "added a widget", "--yes"))
	})
	t.Run("dry run", func(t *testing.T) {
		g := fixture(t, user)
		publicationGit(t, "add", "widget.txt")
		g.check("commit/dry_run.txt", g.run("", "commit", "-m", msg, "--dry-run"))
	})
}

func TestGoldenPushCommand(t *testing.T) {
	const user = "autonomy: {local: confirm, remote: confirm}\n"
	t.Run("prepare", func(t *testing.T) {
		g := goldenPublicationRepo(t, user)
		g.check("push/prepare.txt", g.run("", "push", "--prepare"))
	})
	t.Run("yes", func(t *testing.T) {
		g := goldenPublicationRepo(t, user)
		bare := os.Getenv("AGENTFLOW_TEST_BARE")
		g.check("push/yes.txt", g.run("", "push", "--yes"))
		if refs := publicationGit(t, "--git-dir="+bare, "for-each-ref", "--format=%(refname)"); !strings.Contains(refs, "refs/heads/feature/pr") {
			t.Fatalf("push published %q", refs)
		}
	})
	t.Run("suggest", func(t *testing.T) {
		g := goldenPublicationRepo(t, "autonomy: {remote: suggest}\n")
		g.check("push/suggest.txt", g.run("", "push"))
	})
}

func TestGoldenPRCommand(t *testing.T) {
	const user = "autonomy: {local: confirm, remote: confirm}\n"
	args := []string{"--title", publicationTitle, "--body", publicationBody}
	t.Run("prepare", func(t *testing.T) {
		g := goldenPublicationRepo(t, user)
		g.check("pr/prepare.txt", g.run("", "pr", append([]string{"--prepare"}, args...)...))
	})
	t.Run("prepare draft with reviewers and labels", func(t *testing.T) {
		g := goldenPublicationRepo(t, user)
		more := append([]string{"--prepare", "--draft", "--reviewer", "team:platform", "--label", "quota"}, args...)
		g.check("pr/prepare_draft.txt", g.run("", "pr", more...))
	})
	t.Run("yes", func(t *testing.T) {
		g := goldenPublicationRepo(t, user)
		sha := publicationGit(t, "rev-parse", "HEAD")
		var opened []string
		original := http.DefaultTransport
		http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			if r.Method != "POST" || r.URL.String() != "https://api.github.com/repos/team/app/pulls" {
				t.Errorf("unexpected GitHub request %s %s", r.Method, r.URL)
				http.NotFound(w, r)
				return w.Result(), nil
			}
			opened = append(opened, r.URL.Path)
			fmt.Fprintf(w, `{"number":7,"html_url":"https://github.com/team/app/pull/7","draft":false,"head":{"sha":%q}}`, sha)
			return w.Result(), nil
		})
		t.Cleanup(func() { http.DefaultTransport = original })
		g.check("pr/yes.txt", g.run("", "pr", append(args, "--yes")...))
		if len(opened) != 1 {
			t.Fatalf("opened %d pull requests, want 1", len(opened))
		}
	})
	t.Run("suggest", func(t *testing.T) {
		g := goldenPublicationRepo(t, "autonomy: {remote: suggest}\n")
		g.check("pr/suggest.txt", g.run("", "pr", args...))
	})
	t.Run("title that breaks the convention", func(t *testing.T) {
		g := goldenPublicationRepo(t, user)
		g.check("pr/invalid_title.txt", g.run("", "pr", "--prepare", "--title", "Publish the branch", "--body", publicationBody))
	})
}

func TestGoldenPolicyExplainCommand(t *testing.T) {
	const optedIn = publicationPolicy + publicationOptIn
	t.Run("commit", func(t *testing.T) {
		g := goldenPublicationRepo(t, "autonomy: {local: auto, remote: auto, actions: {push: suggest}}\n")
		g.check("policy_explain/commit.txt", g.run("", "policy", "explain", "commit"))
		g.check("policy_explain/push.txt", g.run("", "policy", "explain", "push"))
	})
	t.Run("PR publication opt-in", func(t *testing.T) {
		goldentest.Isolate(t)
		t.Setenv("HOME", t.TempDir())
		publicationRepository(t, optedIn, optedIn, publicationUser)
		g := newGoldenCLI(t)
		g.check("policy_explain/pr_open_opt_in.txt", g.run("", "policy", "explain", "pr_open"))
	})
	t.Run("unknown action", func(t *testing.T) {
		g := goldenPublicationRepo(t, "autonomy: {local: auto}\n")
		g.check("policy_explain/unknown_action.txt", g.run("", "policy", "explain", "teleport"))
	})
}
