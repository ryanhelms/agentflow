package cli

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/goldentest"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// goldenAI is a worktree with the fake ACP agent ready as harness "demo",
// and a Manager that hosts detached sessions in this process. Commands run
// in the worktree.
type goldenAI struct {
	*goldenCLI
	home, wt, primary string
	m                 aisession.Manager
	d                 *aisessiontest.Detacher
}

func newGoldenAI(t *testing.T, steps []aisessiontest.Step) *goldenAI {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh script")
	}
	goldentest.Isolate(t)
	agent := aisessiontest.Install(t, "fake-agent", aisessiontest.Script{Steps: steps})
	home := aisessiontest.Home(t, aisessiontest.Settings{
		AgentStart: true,
		Overrides:  map[string]harnessdef.Definition{"demo": {Name: "Demo", Command: "fake-agent"}},
		Autonomy:   config.Autonomy{Local: config.Confirm, Remote: config.Confirm},
	}, agent.Dir)
	aisessiontest.Ready(t, home, "demo")
	wt, primary := aisessiontest.Worktree(t)
	t.Chdir(wt)
	detach, d := aisessiontest.InProcessDetach(t)
	m := aisession.NewManager(aisession.Options{Home: home, Version: "test", Detach: detach})
	d.Use(m)
	old := ops.AIOptions
	ops.AIOptions = func(a *app.App) aisession.Options {
		o := ops.AISessionOptions(a)
		o.Detach = detach
		return o
	}
	t.Cleanup(func() { ops.AIOptions = old })
	g := &goldenAI{goldenCLI: newGoldenCLI(t), home: home, wt: wt, primary: primary, m: m, d: d}
	g.norm.Path("<primary>", primary).Path("<fake-agent>", agent.Dir).Path("<tmp>", os.TempDir())
	// The names passed depend on the machine's environment (LANG, LC_*,
	// XDG_*); the session environment tests pin them.
	g.norm.ElideArray("env_names")
	return g
}

// waitAsk starts a detached session as a person and waits for its ask.
func (g *goldenAI) waitAsk(t *testing.T) (aisession.Session, aisession.AskRecord) {
	t.Helper()
	ctx := aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceTerminal, Client: "cli"})
	s, err := g.m.Start(ctx, aisession.StartRequest{Harness: "demo", Prompt: "write", Dir: g.wt, Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, asks, err := g.m.Get(ctx, s.ID); err == nil && len(asks) > 0 {
			return s, asks[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no ask")
	return s, aisession.AskRecord{}
}

func goldenWrite(path, content string) aisessiontest.Step {
	return aisessiontest.Step{Call: &aisessiontest.Call{Method: acp.MethodWriteTextFile, Params: aisessiontest.Params(map[string]any{
		"sessionId": "$SESSION", "path": "$CWD/" + path, "content": content})}}
}

// TestGoldenAIRun pins what ai run prints: an agent's confirmation summary,
// declined (nothing starts) and accepted (a detached session), and the
// refusal in the primary checkout.
func TestGoldenAIRun(t *testing.T) {
	g := newGoldenAI(t, []aisessiontest.Step{{Update: aisessiontest.Message("hi")}})
	setTerminal := goldenPerson(t)
	setTerminal(false)
	t.Setenv("CLAUDECODE", "1")
	g.check("ai/run_agent_declined.txt", g.run("n\n", "ai", "run", "--harness", "demo", "say hi"))
	if list, _ := g.m.List(context.Background()); len(list) != 0 {
		t.Fatalf("a declined run started %d sessions", len(list))
	}
	g.check("ai/run_agent.txt", g.run("y\n", "ai", "run", "--harness", "demo", "say hi"))
	t.Setenv("CLAUDECODE", "")
	list, _ := g.m.List(context.Background())
	if len(list) != 1 || list[0].StartedBy.Kind != aisession.RequesterAgent || list[0].Ceiling != "confirm" {
		t.Fatalf("sessions %+v", list)
	}
	g.d.Wait(t, list[0].ID, 60*time.Second)
	t.Chdir(g.primary)
	g.check("ai/run_refused_primary_checkout.txt", g.run("", "ai", "run", "--harness", "demo", "--json", "say hi"))
}

// TestGoldenAIRespond pins what ai respond prints: an agent's allow is
// refused, a person's allow at a terminal shows the whole request with its
// change and asks, and a reject's result.
func TestGoldenAIRespond(t *testing.T) {
	g := newGoldenAI(t, []aisessiontest.Step{goldenWrite("src/hello.txt", "hello from the agent\n"), goldenWrite("src/second.txt", "second\n")})
	setTerminal := goldenPerson(t)
	s, ask := g.waitAsk(t)

	t.Setenv("CLAUDECODE", "1")
	g.check("ai/respond_allow_refused_agent.txt", g.run("y\n", "ai", "respond", string(s.ID), ask.ID, "--allow"))
	t.Setenv("CLAUDECODE", "")
	setTerminal(true)
	g.check("ai/respond_allow_person.txt", g.run("y\n", "ai", "respond", string(s.ID), ask.ID, "--allow", "--json"))

	deadline := time.Now().Add(30 * time.Second)
	var second aisession.AskRecord
	for time.Now().Before(deadline) {
		if _, asks, err := g.m.Get(context.Background(), s.ID); err == nil && len(asks) > 0 && asks[0].ID != ask.ID {
			second = asks[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if second.ID == "" {
		t.Fatal("no second ask")
	}
	setTerminal(false)
	t.Setenv("CLAUDECODE", "1")
	g.check("ai/respond_reject_agent.txt", g.run("", "ai", "respond", string(s.ID), second.ID, "--reject", "--json"))
	t.Setenv("CLAUDECODE", "")
	g.d.Wait(t, s.ID, 60*time.Second)
}

// TestGoldenAICancel pins ai cancel: the confirmation summary under the
// confirm policy, then the result.
func TestGoldenAICancel(t *testing.T) {
	g := newGoldenAI(t, []aisessiontest.Step{goldenWrite("src/later.txt", "x\n"), {WaitCancel: true}})
	setTerminal := goldenPerson(t)
	setTerminal(true)
	s, _ := g.waitAsk(t)
	g.check("ai/cancel_declined.txt", g.run("n\n", "ai", "cancel", string(s.ID)))
	g.check("ai/cancel.txt", g.run("y\n", "ai", "cancel", string(s.ID), "--json"))
	if ended := g.d.Wait(t, s.ID, 60*time.Second); ended.Code != aisession.CodeCancelled {
		t.Errorf("session ended %s", ended.Code)
	}
}
