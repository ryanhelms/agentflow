package aisession_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// otherHome is a manager on a fresh AGENTFLOW_HOME of its own (what a
// caller that sets AGENTFLOW_HOME per start gets), with the same account
// home and fake agent as r. home is the account home itself when it is
// acct.
type otherHome struct {
	home string
	m    aisession.Manager
	d    *aisessiontest.Detacher
}

func newOtherHome(t *testing.T, r *rig, acct, home string, set aisessiontest.Settings) *otherHome {
	t.Helper()
	if set.Overrides == nil {
		set.Overrides = map[string]harnessdef.Definition{}
	}
	set.Overrides["demo"] = harnessdef.Definition{Name: "Demo", Command: "fake-agent"}
	if home == "" {
		home = aisessiontest.Home(t, set, r.agent.Dir)
	} else {
		aisessiontest.Write(t, home, set)
	}
	aisessiontest.Ready(t, home, "demo")
	detach, d := aisessiontest.InProcessDetach(t)
	m := aisession.NewManager(aisession.Options{Home: home, Version: "test", Detach: detach, AccountHome: acct,
		WorktreeID: func(context.Context, string) (string, error) { return "wt-test", nil }})
	d.Use(m)
	return &otherHome{home: home, m: m, d: d}
}

func (o *otherHome) start(ctx context.Context, dir string) (aisession.Session, error) {
	return o.m.Start(ctx, aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: dir, Detach: true})
}

// waitStarted waits until session id of m is past starting.
func waitStarted(t *testing.T, m aisession.Manager, id aisession.ID) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s, _, err := m.Get(person(context.Background()), id)
		if err == nil && s.State != aisession.StateStarting && s.State != aisession.StateInitializing && s.State != aisession.StateCreating {
			if s.State == aisession.StateEnded {
				t.Fatalf("session %s ended early: %s", id, s.Code)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s did not start", id)
}

func stop(t *testing.T, m aisession.Manager, d *aisessiontest.Detacher, id aisession.ID) {
	t.Helper()
	_, _ = m.Cancel(person(context.Background()), id)
	d.Wait(t, id, 60*time.Second)
}

// otherWorktree is a task worktree other than the rig's.
func otherWorktree(t *testing.T) string {
	t.Helper()
	wt, _ := aisessiontest.Worktree(t)
	return wt
}

// A fresh AGENTFLOW_HOME per start does not lift the caps: every session
// whose home is not the account's leaves a stub in the account store, so
// a start from any other home (or from the account home) counts it, in
// every order.
func TestCapsHoldAcrossFreshHomes(t *testing.T) {
	one, four := 1, 4
	waiting := aisessiontest.Script{Steps: []aisessiontest.Step{{WaitCancel: true}}}
	limits := func(n *int) aisessiontest.Settings {
		return aisessiontest.Settings{AgentStart: true, Sessions: config.UserAISessions{MaxRunning: n}}
	}
	cases := []struct {
		name      string
		firstAcct bool // the first session runs in the account home
		thenAcct  bool // the second starts in the account home
		max       *int
		sameWT    bool
		want      string
	}{
		{"home A then home B, same worktree", false, false, &four, true, aisession.RefuseLimitWorktree},
		{"home A then home B, max_running 1", false, false, &one, false, aisession.RefuseLimitRunning},
		{"home B then the account home", false, true, &one, false, aisession.RefuseLimitRunning},
		{"the account home then home B", true, false, &one, false, aisession.RefuseLimitRunning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, acct := accountRig(t, waiting, limits(c.max), limits(c.max))
			first := newOtherHome(t, r, acct, "", limits(c.max))
			if c.firstAcct {
				first = newOtherHome(t, r, acct, acct, limits(c.max))
			}
			s1, err := first.start(person(context.Background()), r.wt)
			if err != nil {
				t.Fatalf("first start: %v", err)
			}
			waitStarted(t, first.m, s1.ID)
			second := newOtherHome(t, r, acct, "", limits(c.max))
			if c.thenAcct {
				second = newOtherHome(t, r, acct, acct, limits(c.max))
			}
			dir := r.wt
			if !c.sameWT {
				dir = otherWorktree(t)
			}
			s2, err := second.start(person(context.Background()), dir)
			if err == nil {
				t.Errorf("a second session started past the caps (%s, %s)", s1.ID, s2.ID)
				stop(t, second.m, second.d, s2.ID)
			} else if code := refusal(t, err); code != c.want {
				t.Errorf("second start = %s, want %s", code, c.want)
			}
			stop(t, first.m, first.d, s1.ID)
			// Its stub ended with it: the next start runs.
			s3, err := second.start(person(context.Background()), dir)
			if err != nil {
				t.Fatalf("start after the first ended: %v", err)
			}
			stop(t, second.m, second.d, s3.ID)
		})
	}
}

// The hourly start rate counts the starts of every home.
func TestStartRateHoldsAcrossFreshHomes(t *testing.T) {
	r, acct := accountRig(t, aisessiontest.Script{}, aisessiontest.Settings{}, aisessiontest.Settings{})
	for i := range aisession.StartsPerHour {
		o := newOtherHome(t, r, acct, "", aisessiontest.Settings{})
		s, err := o.start(person(context.Background()), r.wt)
		if err != nil {
			t.Fatalf("start %d: %v", i+1, err)
		}
		o.d.Wait(t, s.ID, 60*time.Second)
	}
	o := newOtherHome(t, r, acct, "", aisessiontest.Settings{})
	_, err := o.start(person(context.Background()), r.wt)
	if code := refusal(t, err); code != aisession.RefuseLimitRate {
		t.Errorf("start %d from a fresh home = %s, want limit_rate", aisession.StartsPerHour+1, code)
	}
}

// A stub holds no content and no reader of the account store sees it.
func TestAccountStubIsHiddenAndHoldsNoContent(t *testing.T) {
	r, acct := accountRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{{WaitCancel: true}}}, aisessiontest.Settings{}, aisessiontest.Settings{})
	s := r.start(t, person(context.Background()), "a secret prompt")
	waitStarted(t, r.m, s.ID)
	st, err := aisession.OpenStore(acct)
	if err != nil {
		t.Fatal(err)
	}
	if all, err := st.List(); err != nil || len(all) != 0 {
		t.Errorf("account store List = %+v, %v; want no sessions", all, err)
	}
	if _, err := st.Get(s.ID); err == nil {
		t.Error("account store Get found the stub")
	}
	dir := filepath.Join(acct, "ai", "sessions", string(s.ID))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("no stub: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "session.json" && e.Name() != "host.heartbeat" {
			t.Errorf("the stub holds %s", e.Name())
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "session.json"))
	if len(b) == 0 || strings.Contains(string(b), "a secret prompt") || strings.Contains(string(b), r.home) {
		t.Errorf("stub record = %s", b)
	}
	stop(t, r.m, r.d, s.ID)
}

// A home that is not the account's counts each of its own sessions once:
// its record there and its stub in the account store are one session.
func TestCapsCountOneHomesSessionsOnce(t *testing.T) {
	two := 2
	set := aisessiontest.Settings{Sessions: config.UserAISessions{MaxRunning: &two}}
	t.Run("running", func(t *testing.T) {
		r, _ := accountRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{{WaitCancel: true}}}, set, set)
		s1 := r.start(t, person(context.Background()), "one")
		waitStarted(t, r.m, s1.ID)
		s2, err := r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "demo", Prompt: "two", Dir: otherWorktree(t), Detach: true})
		if err != nil {
			t.Errorf("a second session from the same home = %v, want it to start under max_running 2", err)
		} else {
			stop(t, r.m, r.d, s2.ID)
		}
		stop(t, r.m, r.d, s1.ID)
	})
	t.Run("rate", func(t *testing.T) {
		r, _ := accountRig(t, aisessiontest.Script{}, aisessiontest.Settings{}, aisessiontest.Settings{})
		for i := range aisession.StartsPerHour {
			s, err := r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true})
			if err != nil {
				t.Fatalf("start %d from one home: %v", i+1, err)
			}
			r.d.Wait(t, s.ID, 60*time.Second)
		}
		_, err := r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true})
		if code := refusal(t, err); code != aisession.RefuseLimitRate {
			t.Errorf("start %d = %s, want limit_rate", aisession.StartsPerHour+1, code)
		}
	})
}
