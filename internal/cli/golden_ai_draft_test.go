package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/ai/aitest"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// TestGoldenAIDraft pins what ai draft prints: the manifest of a dry run,
// an agent's confirmation declined (nothing starts), --show-prompt refused
// outside a person's terminal, and a feature with no harness refused.
func TestGoldenAIDraft(t *testing.T) {
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
		Features:  config.UserAIFeatures{Commit: config.UserAIFeature{Harness: "drafter"}},
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft", Args: args}}}, bin)
	aisessiontest.Ready(t, home, "drafter")
	wt, primary := aisessiontest.Worktree(t)
	t.Chdir(wt)
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
	aitest.Use(t, &aitest.Feature{GatherFn: func(ctx context.Context, a *app.App, _ ai.Request) ([]ai.Part, error) {
		out, err := a.Repo.Git(ctx, "diff", "--cached", "--no-color")
		return []ai.Part{{Kind: ai.PartDiff, Label: "staged changes", Source: "git", Text: out}}, err
	}})
	if err := os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", wt, "add", "notes.txt").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	g := newGoldenCLI(t)
	g.norm.Path("<primary>", primary).Path("<fake-agent>", bin).Path("<tmp>", os.TempDir())
	setTerminal := goldenPerson(t)
	setTerminal(false)

	t.Setenv("CLAUDECODE", "1")
	g.check("ai/draft_dry_run_agent.txt", g.run("", "ai", "draft", "commit", "--dry-run", "--json"))
	g.check("ai/draft_show_prompt_refused_agent.txt", g.run("", "ai", "draft", "commit", "--dry-run", "--show-prompt"))
	g.check("ai/draft_agent_declined.txt", g.run("n\n", "ai", "draft", "commit"))
	aitest.Use(t, &aitest.Feature{FeatureID: ai.FeaturePR})
	g.check("ai/draft_feature_off.txt", g.run("", "ai", "draft", "pr", "--json"))
	t.Setenv("CLAUDECODE", "")
	if list, _ := m.List(context.Background()); len(list) != 0 {
		t.Fatalf("%d sessions started", len(list))
	}
}
