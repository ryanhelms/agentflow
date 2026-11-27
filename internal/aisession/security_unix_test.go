//go:build unix

package aisession_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/config"
)

// sleeperRig: terminals running "sleeper PIDFILE", which starts a child
// sleep in the background, writes the child's PID and waits: killing
// only the leader would leave the child running.
func sleeperRule() aisessiontest.Settings {
	return aisessiontest.Settings{Rules: map[string]config.AIRule{"sleeper": {Kind: config.AIRuleKindTerminal, Match: "sleeper", Decision: config.AIDecisionAllow}}}
}

func sleeperBin(t *testing.T) string {
	return script(t, "sleeper", "/bin/sleep 60 &\necho $! > \"$1\"\nwait\n")
}

func TestCancelAnswersAsksAndKillsTerminals(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "sleeper", "args": []string{pidFile}}),
		asyncCall(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/x.txt", "content": "x"}),
		{WaitCancel: true},
	}}, sleeperRule(), sleeperBin(t))
	ctx := person(context.Background())
	s := r.start(t, ctx, "cancel me")
	r.waitAsk(t, s.ID)
	pid := waitPID(t, pidFile)
	if _, err := r.m.Cancel(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	s = r.d.Wait(t, s.ID, 60*time.Second)
	if s.Code != aisession.CodeCancelled {
		t.Errorf("session ended %s", s.Code)
	}
	if !hasAnswer(r.items(t, s.ID), aisession.HandlerCancel) {
		t.Error("the waiting ask was not answered cancelled")
	}
	waitGone(t, pid)
}

// TestTerminalKillAndReleaseKillTheGroup: terminal/kill and
// terminal/release kill the whole process group.
func TestTerminalKillAndReleaseKillTheGroup(t *testing.T) {
	dir := t.TempDir()
	kill, release := filepath.Join(dir, "kill.pid"), filepath.Join(dir, "release.pid")
	term := func(m string) aisessiontest.Step {
		return call(m, map[string]any{"sessionId": "$SESSION", "terminalId": "$TERMINAL"})
	}
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "sleeper", "args": []string{kill}}),
		{Sleep: "300ms"},
		term(acp.MethodTerminalKill),
		term(acp.MethodTerminalWaitForExit),
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "sleeper", "args": []string{release}}),
		{Sleep: "300ms"},
		term(acp.MethodTerminalRelease),
		{WaitCancel: true},
	}}, sleeperRule(), sleeperBin(t))
	ctx := person(context.Background())
	s := r.start(t, ctx, "kill")
	waitGone(t, waitPID(t, kill))
	waitGone(t, waitPID(t, release))
	if _, err := r.m.Cancel(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	r.d.Wait(t, s.ID, 60*time.Second)
}

func waitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the terminal never started")
	return 0
}

func waitGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("process %d still runs", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
