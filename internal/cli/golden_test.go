package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/goldentest"
)

// The golden tests pin what each write command prints: the preview a person
// reviews, the result once it is applied, and what goes to standard error. A
// later change to how commands are wired together must not change them
// unnoticed. Regenerate the files with
//
//	go test ./internal/cli -run Golden -update
//
// and review the diff. The operations' own golden tests live in internal/app.

// goldenCLI runs commands in this process and records what they print.
type goldenCLI struct {
	t    *testing.T
	norm *goldentest.Normalizer
}

// newGoldenCLI normalises the current directory, the agentflow home and the
// user's home. Call it after the test has set them up.
func newGoldenCLI(t *testing.T) *goldenCLI {
	t.Helper()
	repo, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home := os.Getenv("HOME")
	norm := goldentest.New()
	norm.Path("<repo>", repo).
		Path("<worktrees>", filepath.Join(home, ".worktrees", filepath.Base(repo))).
		Path("<agentflow-home>", os.Getenv("AGENTFLOW_HOME")).
		Path("<home>", home)
	return &goldenCLI{t: t, norm: norm}
}

// run executes one command with stdin as its standard input.
func (g *goldenCLI) run(stdin, command string, args ...string) goldentest.Run {
	g.t.Helper()
	files := map[string]*os.File{}
	for _, name := range []string{"stdin", "stdout", "stderr"} {
		f, err := os.CreateTemp(g.t.TempDir(), name)
		if err != nil {
			g.t.Fatal(err)
		}
		defer f.Close()
		files[name] = f
	}
	if _, err := files["stdin"].WriteString(stdin); err != nil {
		g.t.Fatal(err)
	}
	if _, err := files["stdin"].Seek(0, 0); err != nil {
		g.t.Fatal(err)
	}
	oldIn, oldOut, oldErr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = files["stdin"], files["stdout"], files["stderr"]
	err := run(g.t.Context(), command, args)
	os.Stdin, os.Stdout, os.Stderr = oldIn, oldOut, oldErr
	read := func(name string) string {
		b, readErr := os.ReadFile(files[name].Name())
		if readErr != nil {
			g.t.Fatal(readErr)
		}
		return string(b)
	}
	line := []string{"agentflow", command}
	for _, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\n\"'$") {
			arg = strconv.Quote(arg)
		}
		line = append(line, arg)
	}
	return goldentest.Run{Command: strings.Join(line, " "), Stdin: stdin, Stdout: read("stdout"), Stderr: read("stderr"), Err: err}
}

// check records r in the golden file name.
func (g *goldenCLI) check(name string, r goldentest.Run) {
	g.t.Helper()
	goldentest.Check(g.t, name, g.norm.Transcript(r))
}

// pending reads the Pending response a command printed to standard output.
func (g *goldenCLI) pending(r goldentest.Run) app.Pending {
	g.t.Helper()
	var p app.Pending
	if r.Err != nil || json.Unmarshal([]byte(r.Stdout), &p) != nil || p.ConfirmToken == "" {
		g.t.Fatalf("%s: no pending response: %q, %v", r.Command, r.Stdout, r.Err)
	}
	return p
}

func goldenGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// goldenEnvironment gives the test its own home directories and isolates it
// from the machine's environment (see goldentest.Isolate).
func goldenEnvironment(t *testing.T, user string) {
	t.Helper()
	goldentest.Isolate(t)
	agentflowHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(agentflowHome, "config.yml"), []byte(user), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTFLOW_HOME", agentflowHome)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GH_TOKEN", "fixture")
	t.Setenv("JIRA_USER", "jira-user")
	t.Setenv("JIRA_TOKEN", "jira-fixture")
}

// goldenJira answers the Jira calls the write commands make about PLAT-1 and
// records every write, so a test can tell a preview from an applied command.
type goldenJira struct {
	t      *testing.T
	writes []string
}

const goldenIssue = `{"key":"PLAT-1","fields":{"summary":"Reach QA","status":{"name":"Open","statusCategory":{"key":"new"}},"priority":{"name":"High"},"issuetype":{"name":"Story"},"assignee":{"displayName":"Dev"}}}`

func (j *goldenJira) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	call := r.Method + " " + r.URL.Path
	if r.URL.Host != "tasks.example" {
		j.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
		return w.Result(), nil
	}
	if r.Method != http.MethodGet {
		j.writes = append(j.writes, call)
	}
	switch call {
	case "GET /rest/api/3/issue/PLAT-1":
		fmt.Fprint(w, goldenIssue)
	case "GET /rest/api/3/issue/PLAT-1/transitions":
		fmt.Fprint(w, `{"transitions":[{"id":"17","name":"Send for test","to":{"name":"QA","statusCategory":{"key":"indeterminate"}}}]}`)
	case "GET /rest/api/3/myself":
		fmt.Fprint(w, `{"accountId":"account-1","displayName":"Dev"}`)
	case "POST /rest/api/3/issue/PLAT-1/transitions", "PUT /rest/api/3/issue/PLAT-1/assignee":
		w.WriteHeader(http.StatusNoContent)
	case "POST /rest/api/3/issue/PLAT-1/comment":
		fmt.Fprint(w, `{"id":"10000"}`)
	default:
		j.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
	return w.Result(), nil
}

// goldenJiraRepo is a repository named app on branch PLAT-1-quota whose tasks
// live in Jira. The caller's working directory is the repository.
func goldenJiraRepo(t *testing.T, user string) (*goldenCLI, *goldenJira) {
	t.Helper()
	goldenEnvironment(t, user)
	repo := filepath.Join(t.TempDir(), "app")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, text string) {
		t.Helper()
		path := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	goldenGit(t, repo, "init", "-q", "-b", "main")
	goldenGit(t, repo, "config", "user.email", "dev@example.org")
	goldenGit(t, repo, "config", "user.name", "Dev")
	goldenGit(t, repo, "commit", "-q", "--allow-empty", "-m", "chore(PLAT-1): init")
	goldenGit(t, repo, "switch", "-q", "-c", "PLAT-1-quota")
	write("api/quota.go", "package api\n")
	write(".agentflow/config.yml", "task_management: {provider: jira, settings: {site: https://tasks.example, projects: [PLAT, OPS]}}\nflow_map: flow.yml\n")
	write("flow.yml", "projects:\n  PLAT:\n    pr.merged: QA\n    order: [Open, QA]\n")
	goldenGit(t, repo, "add", ".")
	goldenGit(t, repo, "commit", "-q", "-m", "feat(PLAT-1): add quota")
	t.Chdir(repo)

	jira := &goldenJira{t: t}
	original := http.DefaultTransport
	http.DefaultTransport = jira
	t.Cleanup(func() { http.DefaultTransport = original })
	return newGoldenCLI(t), jira
}

func TestGoldenJiraCommands(t *testing.T) {
	const user = "autonomy: {remote: confirm, local: auto}\n"
	t.Run("transition", func(t *testing.T) {
		g, jira := goldenJiraRepo(t, user)
		g.check("transition/jira_yes.txt", g.run("", "transition", "PLAT-1", "pr.merged", "--yes"))
		if want := []string{"POST /rest/api/3/issue/PLAT-1/transitions"}; !slices.Equal(jira.writes, want) {
			t.Fatalf("Jira writes = %v, want %v", jira.writes, want)
		}
	})
	t.Run("transition declined", func(t *testing.T) {
		g, jira := goldenJiraRepo(t, user)
		r := g.run("n\n", "transition", "PLAT-1", "pr.merged")
		g.check("transition/jira_declined.txt", r)
		if len(jira.writes) != 0 {
			t.Fatalf("a declined transition wrote to Jira: %v", jira.writes)
		}
	})
	t.Run("transition suggest", func(t *testing.T) {
		g, jira := goldenJiraRepo(t, "autonomy: {remote: suggest}\n")
		g.check("transition/jira_suggest.txt", g.run("", "transition", "PLAT-1", "pr.merged"))
		if len(jira.writes) != 0 {
			t.Fatalf("a suggested transition wrote to Jira: %v", jira.writes)
		}
	})
	t.Run("transition dry run", func(t *testing.T) {
		g, _ := goldenJiraRepo(t, user)
		g.check("transition/jira_dry_run.txt", g.run("", "transition", "PLAT-1", "pr.merged", "--dry-run"))
	})
	t.Run("comment", func(t *testing.T) {
		g, jira := goldenJiraRepo(t, user)
		g.check("comment/jira_yes.txt", g.run("", "comment", "PLAT-1", "Quota check is in review.", "--yes"))
		if want := []string{"POST /rest/api/3/issue/PLAT-1/comment"}; !slices.Equal(jira.writes, want) {
			t.Fatalf("Jira writes = %v, want %v", jira.writes, want)
		}
	})
	t.Run("comment on the branch's task", func(t *testing.T) {
		g, _ := goldenJiraRepo(t, user)
		g.check("comment/jira_branch_task.txt", g.run("", "comment", "Quota check is in review.", "--yes"))
	})
	t.Run("start and finish", func(t *testing.T) {
		g, jira := goldenJiraRepo(t, user)
		started := g.run("", "start", "PLAT-1", "--worktree", "--yes")
		g.check("start/jira_worktree_yes.txt", started)
		if started.Err != nil {
			t.Fatal(started.Err)
		}
		if want := []string{"PUT /rest/api/3/issue/PLAT-1/assignee"}; !slices.Equal(jira.writes, want) {
			t.Fatalf("Jira writes = %v, want %v", jira.writes, want)
		}

		// Land the task's work on main and treat the local main as origin/main.
		// A stand-in gh keeps finish from asking GitHub whether a PR merged.
		repo, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		worktree := filepath.Join(os.Getenv("HOME"), ".worktrees", "app", "PLAT-1-reach-qa")
		if err := os.WriteFile(filepath.Join(worktree, "fix.txt"), []byte("fix\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		goldenGit(t, worktree, "add", "fix.txt")
		goldenGit(t, worktree, "-c", "user.email=dev@example.org", "-c", "user.name=Dev", "commit", "-q", "-m", "fix: add fix")
		goldenGit(t, repo, "switch", "-q", "main")
		goldenGit(t, repo, "merge", "-q", "--no-ff", "-m", "merge", "PLAT-1-reach-qa")
		goldenGit(t, repo, "update-ref", "refs/remotes/origin/main", "main")
		goldenGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
		bin := t.TempDir()
		if err := os.WriteFile(filepath.Join(bin, "gh"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

		g.check("finish/dry_run.txt", g.run("", "finish", "PLAT-1-reach-qa", "--dry-run"))
		finished := g.run("", "finish", "PLAT-1-reach-qa", "--yes")
		g.check("finish/yes.txt", finished)
		if _, err := os.Stat(worktree); !os.IsNotExist(err) {
			t.Fatalf("finish left the worktree: %v", err)
		}
		g.check("finish/already_finished.txt", g.run("", "finish", "PLAT-1-reach-qa", "--yes"))
	})
	t.Run("start suggest", func(t *testing.T) {
		g, _ := goldenJiraRepo(t, "autonomy: {remote: suggest}\n")
		g.check("start/jira_suggest.txt", g.run("", "start", "PLAT-1", "--worktree"))
	})
}
