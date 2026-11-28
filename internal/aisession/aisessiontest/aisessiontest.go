// Package aisessiontest keeps session tests away from real harnesses and
// real programs ("Phase 7 test guard" in
// docs/knowledge/engineering/decisions/event-driven-platform.md).
//
//   - Home points AGENTFLOW_HOME, HOME and PATH at temporary directories,
//     sets CONTINUOUS_INTEGRATION=1 and writes a config.yml with
//     auto_probe, sign_in_check and ai.sessions.agent_start set explicitly.
//   - Worktree makes a temporary repository with a linked worktree.
//   - Install writes a fake ACP session agent (this test binary in the
//     "aisession-agent" child mode) and Ready marks it probed.
//   - InProcessDetach is an Options.Detach that hosts the session on a
//     goroutine of the test, never a new process.
//
// A test binary that uses Install calls jsonrpctest.RunChild first in
// TestMain. The fake is a /bin/sh wrapper, so Install skips on Windows.
package aisessiontest

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/harness/harnesstest"
)

// Settings are what Home writes to config.yml. AgentStart is always
// written: it defaults to on, so a test states it.
type Settings struct {
	AgentStart bool
	Overrides  map[string]harnessdef.Definition
	Sessions   config.UserAISessions // AgentStart here is ignored
	Rules      map[string]config.AIRule
	Autonomy   config.Autonomy
	// Features and Drafts are the phase 8 keys. Empty by default: every
	// draft feature is off unless a test routes one to a fake.
	Features config.UserAIFeatures
	Drafts   config.UserAIDrafts
}

// Home sets AGENTFLOW_HOME, HOME (also the account home) and PATH (dirs,
// plus git) to temporary directories, CONTINUOUS_INTEGRATION=1, and writes
// config.yml from s. It returns the agentflow home. The test cannot be
// parallel.
func Home(tb testing.TB, s Settings, dirs ...string) string {
	tb.Helper()
	home := harnesstest.PATH(tb, dirs...)
	tb.Setenv("CONTINUOUS_INTEGRATION", "1")
	restore := config.OverrideAccountHome(os.Getenv("HOME"))
	tb.Cleanup(restore)
	Write(tb, home, s)
	return home
}

// Write replaces home/config.yml from s.
func Write(tb testing.TB, home string, s Settings) {
	tb.Helper()
	off := false
	start := s.AgentStart
	u := config.User{Autonomy: s.Autonomy}
	u.AI.Harnesses.AutoProbe, u.AI.Harnesses.SignInCheck = &off, &off
	u.AI.Harnesses.Overrides = s.Overrides
	u.AI.Sessions = s.Sessions
	u.AI.Sessions.AgentStart = &start
	u.AI.Permissions.Rules = s.Rules
	u.AI.Features, u.AI.Drafts = s.Features, s.Drafts
	type file struct {
		Autonomy config.Autonomy `yaml:"autonomy,omitempty"`
		AI       config.UserAI   `yaml:"ai"`
	}
	b, err := yaml.Marshal(file{Autonomy: u.Autonomy, AI: u.AI})
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.yml"), b, 0o600); err != nil {
		tb.Fatal(err)
	}
}

// Worktree makes a repository with one commit and a linked worktree on
// branch "task", and returns the worktree root (links resolved) and the
// primary checkout.
func Worktree(tb testing.TB) (worktree, primary string) {
	tb.Helper()
	base := tb.TempDir()
	if r, err := filepath.EvalSymlinks(base); err == nil {
		base = r
	}
	primary = filepath.Join(base, "repo")
	worktree = filepath.Join(base, "wt")
	git := func(dir string, args ...string) {
		tb.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			tb.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
	}
	if err := os.MkdirAll(primary, 0o755); err != nil {
		tb.Fatal(err)
	}
	git(primary, "init", "-q")
	if err := os.WriteFile(filepath.Join(primary, "README.md"), []byte("hello\n"), 0o644); err != nil {
		tb.Fatal(err)
	}
	git(primary, "add", "README.md")
	git(primary, "commit", "-q", "-m", "init")
	git(primary, "worktree", "add", "-q", worktree, "-b", "task")
	return worktree, primary
}

// Ready marks harness id probed ok for its current identity in home's
// probe cache, so harness.Locate finds it ready. It fails the test when
// the harness is not found.
func Ready(tb testing.TB, home, id string) harness.Found {
	tb.Helper()
	f, _, err := harness.Locate(context.Background(), home, "test", nil, id)
	if f.Path == "" {
		tb.Fatalf("aisessiontest.Ready: %s is not found: %v", id, err)
	}
	rec := harness.ProbeRecord{ID: id, Identity: f.Identity, Status: harness.ProbeOK, Code: harness.CodeOK, ProbedAt: time.Now(), ACP: &harness.ACPInfo{ProtocolVersion: 1}}
	if err := harness.NewFileStore(home).Put(rec); err != nil {
		tb.Fatal(err)
	}
	return f
}

// Detacher hosts detached sessions in the test process.
type Detacher struct {
	m    aisession.Manager
	wg   sync.WaitGroup
	mu   sync.Mutex
	done map[aisession.ID]chan aisession.Session
}

// InProcessDetach returns an Options.Detach that runs Manager.Host on a
// goroutine, and the Detacher to wait on. Set its Manager with Use before
// starting a session. Cleanup waits for every hosted session to end.
func InProcessDetach(tb testing.TB) (func(ctx context.Context, id aisession.ID) error, *Detacher) {
	d := &Detacher{done: map[aisession.ID]chan aisession.Session{}}
	tb.Cleanup(func() { d.wg.Wait() })
	return func(ctx context.Context, id aisession.ID) error {
		ch := make(chan aisession.Session, 1)
		d.mu.Lock()
		d.done[id] = ch
		m := d.m
		d.mu.Unlock()
		d.wg.Add(1)
		go func() {
			defer d.wg.Done()
			s, _ := m.Host(context.WithoutCancel(ctx), id, aisession.HostOptions{Mode: aisession.HostDetached})
			ch <- s
		}()
		return nil
	}, d
}

// Use sets the Manager the detacher hosts with.
func (d *Detacher) Use(m aisession.Manager) {
	d.mu.Lock()
	d.m = m
	d.mu.Unlock()
}

// Wait returns the ended session, or fails the test after timeout.
func (d *Detacher) Wait(tb testing.TB, id aisession.ID, timeout time.Duration) aisession.Session {
	tb.Helper()
	d.mu.Lock()
	ch := d.done[id]
	d.mu.Unlock()
	if ch == nil {
		tb.Fatalf("session %s was not detached", id)
	}
	select {
	case s := <-ch:
		return s
	case <-time.After(timeout):
		tb.Fatalf("session %s did not end in %s", id, timeout)
	}
	return aisession.Session{}
}

func skipWindows(tb testing.TB) {
	if runtime.GOOS == "windows" {
		tb.Skip("the fake agent is a /bin/sh wrapper")
	}
}
