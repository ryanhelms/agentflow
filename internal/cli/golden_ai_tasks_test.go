package cli

import (
	"context"
	"io"
	"net/http"
	"os"
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

// TestGoldenAITasks pins what --ai prints on task create and review reply:
// the refusals of anyone but a person at a terminal and of --yes or the
// drafted text, and the person's draft declined at its confirmation, which
// shows what would be sent. Nothing starts and nothing is written.
func TestGoldenAITasks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh wrapper")
	}
	goldentest.Isolate(t)
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args := acptest.Install(t, bin, "fake-draft", acptest.Script{Session: acptest.Draft()})
	home := aisessiontest.Home(t, aisessiontest.Settings{AgentStart: true,
		Autonomy:  config.Autonomy{Local: config.Confirm, Remote: config.Confirm},
		Features:  config.UserAIFeatures{Ticket: config.UserAIFeature{Harness: "drafter"}, Reply: config.UserAIFeature{Harness: "drafter"}},
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft", Args: args}}}, bin)
	aisessiontest.Ready(t, home, "drafter")
	wt, primary := aisessiontest.Worktree(t)
	t.Chdir(wt)
	if err := os.Mkdir(".agentflow", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(".agentflow", "config.yml"), []byte("task_management: {provider: filesystem}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	goldenGit(t, wt, "remote", "add", "origin", "git@github.com:team/app.git")
	t.Setenv("GH_TOKEN", "fixture")
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = taskTransport(func(r *http.Request) (*http.Response, error) {
		body := `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[{"id":"PRRT_one","path":"README.md","line":1,"comments":{"nodes":[{"body":"Why?","author":{"login":"alice"}}]}}]}}}}}`
		if r.Method == http.MethodGet {
			body = `[{"number":7,"html_url":"https://github.com/team/app/pull/7","head":{"ref":"task","sha":"abc"},"base":{"ref":"main"}}]`
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	detach, d := aisessiontest.InProcessDetach(t)
	m := aisession.NewManager(aisession.Options{Home: home, Version: "test", Detach: detach, FinishDraft: ai.Finish})
	d.Use(m)
	old := ops.AIOptions
	ops.AIOptions = func(a *app.App) aisession.Options {
		o := ops.AISessionOptions(a)
		o.Detach = detach
		return o
	}
	t.Cleanup(func() { ops.AIOptions = old })
	g := newGoldenCLI(t)
	g.norm.Path("<primary>", primary).Path("<fake-agent>", bin).Path("<tmp>", os.TempDir())
	setTerminal := goldenPerson(t)

	setTerminal(false)
	t.Setenv("CLAUDECODE", "1")
	g.check("task/ai_needs_person.txt", g.run("", "task", "create", "--ai", "--description", "Add a greeting"))
	g.check("review/ai_needs_person.txt", g.run("", "review", "reply", "--ai"))
	t.Setenv("CLAUDECODE", "")

	setTerminal(true)
	g.check("task/ai_usage_yes.txt", g.run("", "task", "create", "--ai", "--description", "Add a greeting", "--yes"))
	g.check("review/ai_usage_body.txt", g.run("", "review", "reply", "--ai", "PRRT_one", "Done."))
	g.check("task/ai_draft_declined.txt", g.run("n\n", "task", "create", "--ai", "--description", "Add a greeting"))
	g.check("review/ai_draft_declined.txt", g.run("n\n", "review", "reply", "--ai"))
	if list, _ := m.List(context.Background()); len(list) != 0 {
		t.Fatalf("%d sessions started", len(list))
	}
}
