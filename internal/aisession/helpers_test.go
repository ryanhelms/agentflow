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
	return newRigWith(t, s, set, nil, extraPath...)
}

// newRigWith is newRig with a change to the manager's Options. A "demo"
// override in set is kept (its Command must be "fake-agent").
func newRigWith(t *testing.T, s aisessiontest.Script, set aisessiontest.Settings, opts func(*aisession.Options), extraPath ...string) *rig {
	t.Helper()
	agent := aisessiontest.Install(t, "fake-agent", s)
	if set.Overrides == nil {
		set.Overrides = map[string]harnessdef.Definition{}
	}
	if _, ok := set.Overrides["demo"]; !ok {
		set.Overrides["demo"] = harnessdef.Definition{Name: "Demo", Command: "fake-agent"}
	}
	home := aisessiontest.Home(t, set, append([]string{agent.Dir}, extraPath...)...)
	aisessiontest.Ready(t, home, "demo")
	wt, primary := aisessiontest.Worktree(t)
	detach, d := aisessiontest.InProcessDetach(t)
	r := &rig{home: home, wt: wt, primary: primary, agent: agent, d: d, events: make(chan bus.Event, 1024), settings: set}
	o := aisession.Options{
		Home: home, Version: "test", Detach: detach,
		Emit: func(_ context.Context, e bus.Event) error {
			select {
			case r.events <- e:
			default:
			}
			return nil
		},
		WorktreeID: func(context.Context, string) (string, error) { return "wt-test", nil },
	}
	if opts != nil {
		opts(&o)
	}
	r.m = aisession.NewManager(o)
	d.Use(r.m)
	return r
}

// start starts a detached session as ctx's requester.
func (r *rig) start(t *testing.T, ctx context.Context, prompt string) aisession.Session {
	t.Helper()
	s, err := r.m.Start(ctx, aisession.StartRequest{Harness: "demo", Prompt: prompt, Dir: r.wt, Detach: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	return s
}

// waitAsk waits for session id's first waiting ask.
func (r *rig) waitAsk(t *testing.T, id aisession.ID) aisession.AskRecord {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, asks, err := r.m.Get(person(context.Background()), id); err == nil && len(asks) > 0 {
			return asks[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s held no ask", id)
	return aisession.AskRecord{}
}

// drain returns the events emitted so far.
func (r *rig) drain() []bus.Event {
	var out []bus.Event
	for {
		select {
		case e := <-r.events:
			out = append(out, e)
		default:
			return out
		}
	}
}

// items returns session id's whole transcript.
func (r *rig) items(t *testing.T, id aisession.ID) []aisession.Item {
	t.Helper()
	items, _, err := r.m.Items(context.Background(), id, 0, 10000)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

// script writes an executable shell script into a fresh temporary folder
// and returns the folder.
func script(t *testing.T, name, body string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func call(method string, params map[string]any) aisessiontest.Step {
	return aisessiontest.Step{Call: &aisessiontest.Call{Method: method, Params: aisessiontest.Params(params)}}
}

func asyncCall(method string, params map[string]any) aisessiontest.Step {
	s := call(method, params)
	s.Call.Async = true
	return s
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
