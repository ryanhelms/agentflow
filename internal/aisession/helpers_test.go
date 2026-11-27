package aisession_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// rig is one test's session setup: a fake agent on PATH, a home, a
// worktree, a manager hosting detached sessions in the test process.
type rig struct {
	home, wt, primary string
	agent             aisessiontest.Installed
	m                 aisession.Manager
	d                 *aisessiontest.Detacher
	events            chan bus.Event
	settings          aisessiontest.Settings
}

func newRig(t *testing.T, s aisessiontest.Script, set aisessiontest.Settings, extraPath ...string) *rig {
	t.Helper()
	agent := aisessiontest.Install(t, "fake-agent", s)
	if set.Overrides == nil {
		set.Overrides = map[string]harnessdef.Definition{}
	}
	set.Overrides["demo"] = harnessdef.Definition{Name: "Demo", Command: "fake-agent"}
	home := aisessiontest.Home(t, set, append([]string{agent.Dir}, extraPath...)...)
	aisessiontest.Ready(t, home, "demo")
	wt, primary := aisessiontest.Worktree(t)
	detach, d := aisessiontest.InProcessDetach(t)
	r := &rig{home: home, wt: wt, primary: primary, agent: agent, d: d, events: make(chan bus.Event, 1024), settings: set}
	r.m = aisession.NewManager(aisession.Options{
		Home: home, Version: "test", Detach: detach,
		Emit: func(_ context.Context, e bus.Event) error {
			select {
			case r.events <- e:
			default:
			}
			return nil
		},
		WorktreeID: func(context.Context, string) (string, error) { return "wt-test", nil },
	})
	d.Use(r.m)
	return r
}

func person(ctx context.Context) context.Context {
	return aisession.WithRequester(ctx, aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceTerminal, Client: "cli"})
}

func agentCtx(ctx context.Context) context.Context {
	return aisession.WithRequester(ctx, aisession.Requester{Kind: aisession.RequesterAgent, Surface: aisession.SurfaceMCP, Client: "claude"})
}

// run starts a detached session as who and waits for it to end.
func (r *rig) run(t *testing.T, ctx context.Context, prompt string) aisession.Session {
	t.Helper()
	s, err := r.m.Start(ctx, aisession.StartRequest{Harness: "demo", Prompt: prompt, Dir: r.wt, Detach: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return r.d.Wait(t, s.ID, 60*time.Second)
}

// answerAll answers every ask of session id with d as ctx's requester,
// until the session ends.
func (r *rig) answerAll(t *testing.T, ctx context.Context, id aisession.ID, d aisession.Decision, stop <-chan struct{}) {
	t.Helper()
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
			_, asks, err := r.m.Get(ctx, id)
			if err != nil {
				continue
			}
			for _, a := range asks {
				_, _ = r.m.Answer(ctx, aisession.AnswerRequest{Session: id, Ask: a.ID, Decision: d, Fingerprint: a.Fingerprint})
			}
		}
	}()
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
