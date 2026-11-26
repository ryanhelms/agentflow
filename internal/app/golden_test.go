package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ryanhelms/agentflow/internal/flowmap"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// The golden tests pin what write operations return: the Pending response of a
// gated operation, the result once it is applied, and the policy explanation.
// A later change to how operations are wired together must not change them
// unnoticed. Regenerate the files with
//
//	go test ./internal/app -run Golden -update
//
// and review the diff. The CLI's own golden tests live in internal/cli.

// goldenFixture holds the normaliser for one test.
type goldenFixture struct {
	t    *testing.T
	norm *goldentest.Normalizer
}

func newGoldenFixture(t *testing.T, a *App) *goldenFixture {
	t.Helper()
	norm := goldentest.New()
	norm.Path("<repo>", a.Repo.Dir).
		Path("<worktrees>", filepath.Dir(a.taskWorktreePath(t.Context(), "branch"))).
		Path("<agentflow-home>", a.Home).
		Path("<home>", os.Getenv("HOME")).
		Word("<repo-name>", filepath.Base(a.Repo.Dir))
	return &goldenFixture{t: t, norm: norm}
}

// json renders v the way the CLI prints a result and checks it against name.
func (g *goldenFixture) json(name string, v any) {
	g.t.Helper()
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		g.t.Fatal(err)
	}
	goldentest.Check(g.t, name, g.norm.Text(out.String()))
}

// reopen opens a's repository again with personal configuration in user, so the
// autonomy levels come from the file a developer would edit.
func reopen(t *testing.T, a *App, user string) *App {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.Home, "config.yml"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := Open(t.Context(), a.Repo.Dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	b.tasks = a.tasks
	return b
}

// goldenJira answers the Jira calls the write operations make about PLAT-1 and
// records every write, so a test can tell a preview from an applied operation.
type goldenJira struct {
	t      *testing.T
	writes []string
}

func (j *goldenJira) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Host != "tasks.example" {
		j.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
		return
	}
	call := r.Method + " " + r.URL.Path
	if r.Method != http.MethodGet {
		j.writes = append(j.writes, call)
	}
	switch call {
	case "GET /rest/api/3/issue/PLAT-1":
		fmt.Fprint(w, fixtureIssue)
	case "GET /rest/api/3/issue/PLAT-1/transitions":
		fmt.Fprint(w, `{"transitions":[{"id":"17","name":"Send for test","to":{"name":"QA","statusCategory":{"key":"indeterminate"}}}]}`)
	case "GET /rest/api/3/myself":
		fmt.Fprint(w, `{"accountId":"account-1","displayName":"Dev"}`)
	case "GET /rest/api/3/issue/PLAT-1/remotelink", "GET /rest/api/3/issue/OPS-4/remotelink":
		fmt.Fprint(w, `[]`)
	case "POST /rest/api/3/issue/PLAT-1/transitions", "PUT /rest/api/3/issue/PLAT-1/assignee":
		w.WriteHeader(http.StatusNoContent)
	case "POST /rest/api/3/issue/PLAT-1/comment":
		fmt.Fprint(w, `{"id":"10000"}`)
	default:
		j.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

// goldenJiraApp is a Jira-backed app whose personal autonomy comes from user.
func goldenJiraApp(t *testing.T, user string) (*App, *goldenJira, *goldenFixture) {
	t.Helper()
	goldentest.Isolate(t)
	jira := &goldenJira{t: t}
	a := reopen(t, legacyApp(t, jira.ServeHTTP), user)
	return a, jira, newGoldenFixture(t, a)
}

// goldenIssueApp is a GitHub Issues app (task 42 of org/repo) whose personal autonomy comes from user.
func goldenIssueApp(t *testing.T, user string) (*App, *fakeIssues, *goldenFixture) {
	t.Helper()
	goldentest.Isolate(t)
	a, issues := issueApp(t)
	a = reopen(t, a, user)
	return a, issues, newGoldenFixture(t, a)
}

func TestGoldenCommit(t *testing.T) {
	const msg = "fix(PLAT-1): bound quota"
	paths := []string{"new.txt"}
	// fixture returns an app on a branch with one untracked file to commit.
	fixture := func(t *testing.T, user string) (*App, *goldenFixture) {
		t.Helper()
		goldentest.Isolate(t)
		a, err := Open(t.Context(), newRepo(t), "test")
		if err != nil {
			t.Fatal(err)
		}
		a = reopen(t, a, user)
		if err := os.WriteFile(filepath.Join(a.Repo.Dir, "new.txt"), []byte("quota\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return a, newGoldenFixture(t, a)
	}
	t.Run("confirm then commit", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {local: confirm}\n")
		pending, err := a.Commit(t.Context(), msg, paths, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("commit/needs_confirmation.json", pending)
		done, err := a.Commit(t.Context(), msg, paths, confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("commit/committed.json", done)
	})
	t.Run("suggest", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {local: suggest}\n")
		pending, err := a.Commit(t.Context(), msg, paths, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("commit/suggest.json", pending)
	})
	t.Run("invalid message", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {local: confirm}\n")
		out, err := a.Commit(t.Context(), "added some quota", paths, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("commit/invalid.json", out)
	})
	t.Run("prepare under confirm", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {local: confirm}\n")
		out, err := a.CommitPrepare(t.Context(), msg, paths)
		if err != nil {
			t.Fatal(err)
		}
		g.json("commit_prepare/needs_confirmation.json", out)
	})
	t.Run("prepare under auto", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {local: auto}\n")
		out, err := a.CommitPrepare(t.Context(), msg, paths)
		if err != nil {
			t.Fatal(err)
		}
		g.json("commit_prepare/prepared.json", out)
	})
}

// goldenPublication gives the app a GitHub remote for its branch. Pushing goes
// to a local bare repository through a stand-in for ssh, so nothing leaves the machine.
func goldenPublication(t *testing.T, a *App) {
	t.Helper()
	remote := t.TempDir()
	mustGit(t, a.Repo, "init", "--bare", remote)
	mustGit(t, a.Repo, "remote", "add", "origin", "git@github.com:team/app.git")
	const bridge = `#!/bin/sh
case "$*" in
	*git-receive-pack*) exec git-receive-pack "$FIXTURE_BARE" ;;
	*git-upload-pack*) exec git-upload-pack "$FIXTURE_BARE" ;;
	*) exit 1 ;;
esac
`
	script := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(script, []byte(bridge), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_SSH_COMMAND", script)
	t.Setenv("GIT_SSH_VARIANT", "simple")
	t.Setenv("FIXTURE_BARE", remote)
}

func TestGoldenPush(t *testing.T) {
	fixture := func(t *testing.T, user string) (*App, *goldenFixture) {
		t.Helper()
		a, _, g := goldenJiraApp(t, user)
		goldenPublication(t, a)
		return a, g
	}
	t.Run("prepare under confirm", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {remote: confirm}\n")
		out, err := a.PushPrepare(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		g.json("push_prepare/needs_confirmation.json", out)
	})
	t.Run("prepare under auto", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {remote: auto}\n")
		out, err := a.PushPrepare(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		g.json("push_prepare/prepared.json", out)
	})
	t.Run("suggest", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {remote: suggest}\n")
		out, err := a.PushPrepare(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		g.json("push_prepare/suggest.json", out)
	})
	t.Run("confirm then push", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {remote: confirm}\n")
		pending, err := a.Push(t.Context(), "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("push/needs_confirmation.json", pending)
		done, err := a.Push(t.Context(), confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("push/pushed.json", done)
	})
}

func TestGoldenPROpenPrepare(t *testing.T) {
	in := PROpenInput{Title: "feat(PLAT-1): bound quota", Body: "Adds the quota check."}
	fixture := func(t *testing.T, user string) (*App, *goldenFixture) {
		t.Helper()
		a, _, g := goldenJiraApp(t, user)
		goldenPublication(t, a)
		return a, g
	}
	t.Run("confirm", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {remote: confirm}\n")
		out, err := a.PRPrepare(t.Context(), in)
		if err != nil {
			t.Fatal(err)
		}
		g.json("pr_prepare/needs_confirmation.json", out)
	})
	t.Run("suggest", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {remote: suggest}\n")
		draft := true
		out, err := a.PRPrepare(t.Context(), PROpenInput{Title: in.Title, Body: in.Body, Draft: &draft, Reviewers: []string{"team:platform"}, Labels: []string{"quota"}})
		if err != nil {
			t.Fatal(err)
		}
		g.json("pr_prepare/suggest_draft.json", out)
	})
	t.Run("title that breaks the convention", func(t *testing.T) {
		a, g := fixture(t, "autonomy: {remote: confirm}\n")
		out, err := a.PRPrepare(t.Context(), PROpenInput{Title: "Quota check", Body: in.Body})
		if err != nil {
			t.Fatal(err)
		}
		g.json("pr_prepare/invalid_title.json", out)
	})
}

func TestGoldenTransition(t *testing.T) {
	t.Run("jira", func(t *testing.T) {
		a, jira, g := goldenJiraApp(t, "autonomy: {remote: confirm}\n")
		pending, err := a.Transition(t.Context(), "PLAT-1", flowmap.PRMerged, false, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("transition/jira_needs_confirmation.json", pending)
		if len(jira.writes) != 0 {
			t.Fatalf("preview wrote to Jira: %v", jira.writes)
		}
		done, err := a.Transition(t.Context(), "PLAT-1", flowmap.PRMerged, false, confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("transition/jira_applied.json", done)
		if want := []string{"POST /rest/api/3/issue/PLAT-1/transitions"}; !slices.Equal(jira.writes, want) {
			t.Fatalf("Jira writes = %v, want %v", jira.writes, want)
		}
	})
	t.Run("jira suggest", func(t *testing.T) {
		a, _, g := goldenJiraApp(t, "autonomy: {remote: suggest}\n")
		out, err := a.Transition(t.Context(), "PLAT-1", flowmap.PRMerged, false, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("transition/jira_suggest.json", out)
	})
	t.Run("jira dry run", func(t *testing.T) {
		a, _, g := goldenJiraApp(t, "autonomy: {remote: confirm}\n")
		out, err := a.Transition(t.Context(), "PLAT-1", flowmap.PRMerged, true, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("transition/jira_dry_run.json", out)
	})
	t.Run("github issue", func(t *testing.T) {
		a, issues, g := goldenIssueApp(t, "autonomy: {remote: confirm}\n")
		pending, err := a.Transition(t.Context(), "#42", flowmap.PRMerged, false, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("transition/issue_needs_confirmation.json", pending)
		if len(issues.writes) != 0 {
			t.Fatalf("preview wrote to the issue: %v", issues.writes)
		}
		done, err := a.Transition(t.Context(), "#42", flowmap.PRMerged, false, confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("transition/issue_applied.json", done)
		if len(issues.writes) != 1 {
			t.Fatalf("issue writes = %v", issues.writes)
		}
	})
}

func TestGoldenComment(t *testing.T) {
	t.Run("jira", func(t *testing.T) {
		a, jira, g := goldenJiraApp(t, "autonomy: {remote: confirm}\n")
		pending, err := a.Comment(t.Context(), "PLAT-1", "Quota check is in review.", "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("comment/jira_needs_confirmation.json", pending)
		if len(jira.writes) != 0 {
			t.Fatalf("preview wrote to Jira: %v", jira.writes)
		}
		done, err := a.Comment(t.Context(), "PLAT-1", "Quota check is in review.", confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("comment/jira_applied.json", done)
		if want := []string{"POST /rest/api/3/issue/PLAT-1/comment"}; !slices.Equal(jira.writes, want) {
			t.Fatalf("Jira writes = %v, want %v", jira.writes, want)
		}
	})
	t.Run("jira suggest", func(t *testing.T) {
		a, _, g := goldenJiraApp(t, "autonomy: {remote: suggest}\n")
		out, err := a.Comment(t.Context(), "PLAT-1", "Quota check is in review.", "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("comment/jira_suggest.json", out)
	})
	t.Run("github issue", func(t *testing.T) {
		a, issues, g := goldenIssueApp(t, "autonomy: {remote: confirm}\n")
		pending, err := a.Comment(t.Context(), "#42", "Quota check is in review.", "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("comment/issue_needs_confirmation.json", pending)
		if len(issues.writes) != 0 {
			t.Fatalf("preview wrote to the issue: %v", issues.writes)
		}
		done, err := a.Comment(t.Context(), "#42", "Quota check is in review.", confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("comment/issue_applied.json", done)
		if len(issues.writes) != 1 {
			t.Fatalf("issue writes = %v", issues.writes)
		}
	})
}

func TestGoldenStart(t *testing.T) {
	t.Run("jira worktree", func(t *testing.T) {
		a, jira, g := goldenJiraApp(t, "autonomy: {remote: confirm, local: auto}\n")
		pending, err := a.Start(t.Context(), "PLAT-1", true, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("start/jira_needs_confirmation.json", pending)
		if len(jira.writes) != 0 {
			t.Fatalf("preview wrote to Jira: %v", jira.writes)
		}
		done, err := a.Start(t.Context(), "PLAT-1", true, confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("start/jira_applied.json", done)
		if want := []string{"PUT /rest/api/3/issue/PLAT-1/assignee"}; !slices.Equal(jira.writes, want) {
			t.Fatalf("Jira writes = %v, want %v", jira.writes, want)
		}
	})
	t.Run("jira suggest", func(t *testing.T) {
		// The fixture's uncommitted configuration makes Start use a worktree even without the flag.
		a, _, g := goldenJiraApp(t, "autonomy: {remote: suggest}\n")
		out, err := a.Start(t.Context(), "PLAT-1", false, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("start/jira_suggest.json", out)
	})
	t.Run("github issue branch", func(t *testing.T) {
		a, issues, g := goldenIssueApp(t, "autonomy: {remote: confirm, local: auto}\n")
		pending, err := a.Start(t.Context(), "#42", false, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("start/issue_branch_needs_confirmation.json", pending)
		done, err := a.Start(t.Context(), "#42", false, confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("start/issue_branch_applied.json", done)
		if len(issues.writes) != 1 {
			t.Fatalf("issue writes = %v", issues.writes)
		}
	})
	t.Run("github issue worktree", func(t *testing.T) {
		a, issues, g := goldenIssueApp(t, "autonomy: {remote: confirm, local: auto}\n")
		pending, err := a.Start(t.Context(), "#42", true, "")
		if err != nil {
			t.Fatal(err)
		}
		g.json("start/issue_needs_confirmation.json", pending)
		if len(issues.writes) != 0 {
			t.Fatalf("preview wrote to the issue: %v", issues.writes)
		}
		done, err := a.Start(t.Context(), "#42", true, confirmation(t, pending))
		if err != nil {
			t.Fatal(err)
		}
		g.json("start/issue_applied.json", done)
		if len(issues.writes) != 1 {
			t.Fatalf("issue writes = %v", issues.writes)
		}
	})
}

func TestGoldenFinish(t *testing.T) {
	a, _, _ := goldenIssueApp(t, "autonomy: {remote: auto, local: auto}\n")
	ctx := t.Context()
	// Start a task in a worktree, land its work on main and treat the local main as origin/main.
	started, err := a.Start(ctx, "#42", true, "")
	if err != nil {
		t.Fatal(err)
	}
	dir, branch := started.(StartResult).Worktree, started.(StartResult).Branch
	if err := os.WriteFile(filepath.Join(dir, "fix.txt"), []byte("fix"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &gitx.Repo{Dir: dir}
	mustGit(t, w, "add", "fix.txt")
	mustGit(t, w, "commit", "-qm", "fix: add fix")
	mustGit(t, a.Repo, "switch", "main")
	mustGit(t, a.Repo, "merge", "-q", "--no-ff", "-m", "merge", branch)
	mustGit(t, a.Repo, "update-ref", "refs/remotes/origin/main", "main")
	mustGit(t, a.Repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	a = reopen(t, a, "autonomy: {remote: confirm}\n")
	g := newGoldenFixture(t, a)
	// Finish with the same checks as Finish, but without fetching or asking GitHub.
	opt := a.worktreeOptions()
	opt.Offline = true

	dry, err := a.finish(ctx, branch, true, "", opt)
	if err != nil {
		t.Fatal(err)
	}
	g.json("finish/dry_run.json", dry)
	pending, err := a.finish(ctx, branch, false, "", opt)
	if err != nil {
		t.Fatal(err)
	}
	g.json("finish/needs_confirmation.json", pending)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("finish removed the worktree before confirmation: %v", err)
	}
	done, err := a.finish(ctx, branch, false, confirmation(t, pending), opt)
	if err != nil {
		t.Fatal(err)
	}
	g.json("finish/finished.json", done)
	if done.(FinishResult).State != worktree.StateFinished {
		t.Fatalf("finish state = %s", done.(FinishResult).State)
	}
	again, err := a.finish(ctx, branch, false, "", opt)
	if err != nil {
		t.Fatal(err)
	}
	g.json("finish/already_finished.json", again)
}

func TestGoldenPolicyExplain(t *testing.T) {
	goldentest.Isolate(t)
	a, err := Open(t.Context(), newRepo(t), "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(a.Repo.Dir, ".agentflow"), 0o700); err != nil {
		t.Fatal(err)
	}
	const repoConfig = "policy: {max_autonomy: {remote: confirm}}\n"
	if err := os.WriteFile(filepath.Join(a.Repo.Dir, ".agentflow/config.yml"), []byte(repoConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	a = reopen(t, a, "autonomy: {local: auto, remote: auto, actions: {push: suggest}}\n")
	g := newGoldenFixture(t, a)
	for _, action := range []string{"commit", "push", "pr_open", "transition"} {
		out, err := a.PolicyExplain(t.Context(), action)
		if err != nil {
			t.Fatal(err)
		}
		g.json("policy_explain/"+action+".json", out)
	}
	if _, err := a.PolicyExplain(t.Context(), "teleport"); err == nil {
		t.Fatal("accepted an unknown action")
	}
	all, err := a.PolicyExplain(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	g.json("policy_explain/all.json", all)
}
