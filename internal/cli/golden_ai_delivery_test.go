package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// The golden tests of `commit --ai`, `pr --ai` and `ac --ai`: the fake agent
// drafts, a person at a terminal answers. ai.draft is auto, so the first
// question of each run is the one about using the draft.

type goldenDrafting struct {
	home, bin string
	settings  aisessiontest.Settings
}

func newGoldenDrafting(t *testing.T) goldenDrafting {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh wrapper")
	}
	goldentest.Isolate(t)
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args := acptest.Install(t, bin, "fake-draft", acptest.Script{Session: acptest.Draft()})
	on := config.UserAIFeature{Harness: "drafter"}
	d := goldenDrafting{bin: bin, settings: aisessiontest.Settings{AgentStart: true,
		Autonomy:  config.Autonomy{Local: config.Confirm, Remote: config.Confirm, Actions: map[string]config.Level{ops.ActionDraft: config.Auto}},
		Features:  config.UserAIFeatures{Commit: on, PR: on, AC: on},
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft", Args: args}}}}
	// The publication fixture's SSH script and git's commit guard hook need a
	// few shell tools. The test PATH stays inside the temporary directory, so
	// link only those into it.
	tools := t.TempDir()
	for _, name := range []string{"sh", "cat", "rm", "cmp"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
			t.Fatal(err)
		}
	}
	d.home = aisessiontest.Home(t, d.settings, bin, tools)
	// A person's draft is hosted in the foreground; the session checks still
	// want a Detach in a test binary.
	detach, hosted := aisessiontest.InProcessDetach(t)
	hosted.Use(aisession.NewManager(aisession.Options{Home: d.home, Version: "test", Detach: detach, FinishDraft: ai.Finish}))
	old := ops.AIOptions
	ops.AIOptions = func(a *app.App) aisession.Options {
		o := ops.AISessionOptions(a)
		o.Detach = detach
		return o
	}
	t.Cleanup(func() { ops.AIOptions = old })
	return d
}

// ready uses the drafting home for the repository in the working directory
// and returns the golden runner for it.
func (d goldenDrafting) ready(t *testing.T) *goldenCLI {
	t.Helper()
	t.Setenv("AGENTFLOW_HOME", d.home)
	aisessiontest.Write(t, d.home, d.settings)
	aisessiontest.Ready(t, d.home, "drafter")
	goldenPerson(t)
	g := newGoldenCLI(t)
	g.norm.Path("<fake-agent>", d.bin).Path("<tmp>", os.TempDir())
	return g
}

// draftingPolicy leaves remote actions to the person's own autonomy, so
// ai.draft can be auto.
const draftingPolicy = publicationConfig + "  max_autonomy: {remote: auto}\n"

// publication is the publication fixture with one committed and one staged
// change on its feature branch.
func (d goldenDrafting) publication(t *testing.T) *goldenCLI {
	t.Helper()
	return d.publicationWith(t, draftingPolicy)
}

func (d goldenDrafting) publicationWith(t *testing.T, policy string) *goldenCLI {
	t.Helper()
	publicationRepository(t, policy, policy, "")
	writePublicationFile(t, "widget.txt", "widget\n")
	publicationGit(t, "add", "widget.txt")
	publicationGit(t, "commit", "-q", "-m", "feat: add widget")
	writePublicationFile(t, "notes.txt", "hello\n")
	publicationGit(t, "add", "notes.txt")
	return d.ready(t)
}

func headSubject(t *testing.T) string {
	t.Helper()
	return publicationGit(t, "log", "-1", "--format=%s")
}

func TestGoldenCommitAI(t *testing.T) {
	t.Run("not sent", func(t *testing.T) {
		// The repository caps remote actions at confirm, so the draft itself
		// is asked first; declining it sends nothing.
		g := newGoldenDrafting(t).publicationWith(t, publicationPolicy)
		g.check("commit/ai_send_declined.txt", g.run("n\n", "commit", "--ai"))
		if entries, _ := os.ReadDir(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "ai", "sessions")); len(entries) != 0 {
			t.Fatalf("%d sessions started", len(entries))
		}
	})
	t.Run("refused for an agent and with --yes", func(t *testing.T) {
		g := newGoldenDrafting(t).publication(t)
		g.check("commit/ai_with_yes.txt", g.run("", "commit", "--ai", "--yes"))
		t.Setenv("CLAUDECODE", "1")
		g.check("commit/ai_needs_person.txt", g.run("", "commit", "--ai"))
		if entries, _ := os.ReadDir(filepath.Join(os.Getenv("AGENTFLOW_HOME"), "ai", "sessions")); len(entries) != 0 {
			t.Fatalf("%d sessions started", len(entries))
		}
	})
	t.Run("prepare", func(t *testing.T) {
		g := newGoldenDrafting(t).publication(t)
		g.check("commit/ai_prepare.txt", g.run("", "commit", "--ai", "--prepare", "--path", "notes.txt"))
		if got := headSubject(t); got != "feat: add widget" {
			t.Fatalf("prepare committed: %q", got)
		}
	})
	t.Run("declined", func(t *testing.T) {
		g := newGoldenDrafting(t).publication(t)
		g.check("commit/ai_declined.txt", g.run("n\n", "commit", "--ai"))
		if got := headSubject(t); got != "feat: add widget" {
			t.Fatalf("a declined draft committed: %q", got)
		}
	})
	t.Run("used, then the commit's own confirmation", func(t *testing.T) {
		g := newGoldenDrafting(t).publication(t)
		g.check("commit/ai_confirm_declined.txt", g.run("y\nn\n", "commit", "--ai", "--path", "notes.txt"))
		if got := headSubject(t); got != "feat: add widget" {
			t.Fatalf("committed without the confirmation: %q", got)
		}
	})
	t.Run("applied", func(t *testing.T) {
		d := newGoldenDrafting(t)
		d.settings.Autonomy.Local = config.Auto
		g := d.publication(t)
		r := g.run("y\n", "commit", "--ai")
		if r.Err != nil || headSubject(t) != "feat: add notes" {
			t.Fatalf("HEAD %q, %v\n%s%s", headSubject(t), r.Err, r.Stdout, r.Stderr)
		}
		if body := publicationGit(t, "log", "-1", "--format=%b"); body != "Add notes.txt with a greeting." {
			t.Errorf("body %q", body)
		}
	})
}

func TestGoldenPRAI(t *testing.T) {
	t.Run("prepare", func(t *testing.T) {
		g := newGoldenDrafting(t).publication(t)
		publicationGit(t, "reset", "-q", "--hard")
		r := g.run("", "pr", "--ai", "--prepare")
		g.check("pr/ai_prepare.txt", r)
	})
	t.Run("declined", func(t *testing.T) {
		g := newGoldenDrafting(t).publication(t)
		g.check("pr/ai_declined.txt", g.run("n\n", "pr", "--ai"))
	})
	t.Run("used", func(t *testing.T) {
		d := newGoldenDrafting(t)
		d.settings.Autonomy.Remote = config.Auto
		d.settings.Autonomy.Actions["pr_open"], d.settings.Autonomy.Actions["pr_open_draft"] = config.Suggest, config.Suggest
		g := d.publication(t)
		publicationGit(t, "reset", "-q", "--hard")
		var requests []string
		original := http.DefaultTransport
		http.DefaultTransport = taskTransport(func(req *http.Request) (*http.Response, error) {
			requests = append(requests, req.Method+" "+req.URL.Path)
			w := httptest.NewRecorder()
			w.WriteHeader(http.StatusNotFound)
			return w.Result(), nil
		})
		t.Cleanup(func() { http.DefaultTransport = original })
		r := g.run("y\n", "pr", "--ai", "--draft", "--label", "quota")
		// suggest prints the commands with the drafted text; nothing is sent.
		if r.Err != nil || !strings.Contains(r.Stdout, "--title 'feat: add notes' --body 'Adds notes.txt.") || !strings.Contains(r.Stdout, "--draft --label 'quota'") {
			t.Fatalf("%v\n%s%s", r.Err, r.Stdout, r.Stderr)
		}
		if len(requests) != 0 {
			t.Errorf("requests %v", requests)
		}
	})
}

// draftTaskRepo is a repository on branch task whose task, with two
// unticked criteria, is a file.
func (d goldenDrafting) draftTaskRepo(t *testing.T) (*goldenCLI, string) {
	t.Helper()
	t.Chdir(t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	publicationGit(t, "init", "--quiet", "--template=", "-b", "main")
	publicationGit(t, "config", "user.name", "Fixture")
	publicationGit(t, "config", "user.email", "test@example.org")
	if err := os.Mkdir(".agentflow", 0o700); err != nil {
		t.Fatal(err)
	}
	writePublicationFile(t, ".agentflow/config.yml", "code_hosting: {provider: none}\ncommit: {require_key: false}\ntask_management: {provider: filesystem}\n")
	publicationGit(t, "add", ".agentflow/config.yml")
	publicationGit(t, "commit", "--quiet", "-m", "chore: configure tasks")
	publicationGit(t, "switch", "--quiet", "-c", "task")
	writePublicationFile(t, "notes.txt", "hello\n")
	publicationGit(t, "add", "notes.txt")
	publicationGit(t, "commit", "--quiet", "-m", "feat: add notes")
	g := d.ready(t)
	a, err := app.Open(t.Context(), ".", "cli")
	if err != nil {
		t.Fatal(err)
	}
	a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Auto, config.Auto
	create := func(token string) (any, error) {
		return a.CreateTicket(t.Context(), "", "", "Greet people", "Acceptance criteria\n[ ] Notes hold a greeting\n[ ] The greeting is translated\n", "", token)
	}
	v, err := create("")
	if pending, ok := v.(*app.Pending); ok && err == nil {
		v, err = create(pending.ConfirmToken)
	}
	created, ok := v.(map[string]string)
	if err != nil || !ok {
		t.Fatalf("create: %#v, %v", v, err)
	}
	publicationGit(t, "config", "--local", "branch.task.agentflowTaskRef", created["key"])
	return g, created["key"]
}

func TestGoldenACAI(t *testing.T) {
	ticked := func(t *testing.T) bool {
		t.Helper()
		a, err := app.Open(t.Context(), ".", "cli")
		if err != nil {
			t.Fatal(err)
		}
		cl, err := a.Checklist(t.Context(), "")
		if err != nil || len(cl.Items) != 2 || cl.Items[1].Done {
			t.Fatalf("checklist %+v, %v", cl, err)
		}
		return cl.Items[0].Done
	}
	t.Run("declined", func(t *testing.T) {
		g, _ := newGoldenDrafting(t).draftTaskRepo(t)
		g.check("ac/ai_declined.txt", g.run("n\n", "ac", "--ai"))
		if ticked(t) {
			t.Fatal("a declined criterion was ticked")
		}
	})
	t.Run("ticked", func(t *testing.T) {
		g, key := newGoldenDrafting(t).draftTaskRepo(t)
		g.check("ac/ai_tick.txt", g.run("y\n", "ac", "--ai", key))
		if !ticked(t) {
			t.Fatal("the criterion was not ticked")
		}
	})
}
