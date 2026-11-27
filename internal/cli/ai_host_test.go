package cli

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// ai host opens the App on the worktree the session record names, whatever
// directory the host process starts in.
func TestAIHostOpensTheRecordsWorktree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh script")
	}
	agent := aisessiontest.Install(t, "fake-agent", aisessiontest.Script{})
	home := aisessiontest.Home(t, aisessiontest.Settings{Overrides: map[string]harnessdef.Definition{"demo": {Name: "Demo", Command: "fake-agent"}}}, agent.Dir)
	aisessiontest.Ready(t, home, "demo")
	wt, _ := aisessiontest.Worktree(t)
	var detached []aisession.ID
	m := aisession.NewManager(aisession.Options{Home: home, Version: "test",
		Detach: func(_ context.Context, id aisession.ID) error { detached = append(detached, id); return nil }})
	ctx := aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceTerminal, Client: "cli"})
	s, err := m.Start(ctx, aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: wt, Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(detached) != 1 || detached[0] != s.ID {
		t.Fatalf("detached %v", detached)
	}
	other, _ := aisessiontest.Worktree(t)
	t.Chdir(other)
	a, err := openHostApp(t.Context(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := filepath.EvalSymlinks(a.Repo.Dir)
	if got != wt {
		t.Errorf("the host opened %s, want the record's worktree %s", got, wt)
	}
	if _, err := openHostApp(t.Context(), aisession.NewID()); err == nil {
		t.Error("an unknown session opened an App")
	}
	// Mark it ended so cleanup leaves no starting record behind.
	st, _ := aisession.OpenStore(home)
	_, _ = st.Update(s.ID, func(x *aisession.Session) error {
		x.State, x.Code = aisession.StateEnded, aisession.CodeLaunch
		return nil
	})
}
