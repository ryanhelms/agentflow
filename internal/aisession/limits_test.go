package aisession_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/config"
)

func skipNotUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent and its programs are /bin/sh scripts")
	}
}

// An agentflow home that others can write is refused home_unsafe before
// anything starts.
func TestHomeUnsafeRefuses(t *testing.T) {
	skipNotUnix(t)
	r := newRig(t, aisessiontest.Script{}, aisessiontest.Settings{})
	if err := os.Chmod(r.home, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.home, 0o700) })
	ctx := person(context.Background())
	if _, err := r.m.Prepare(ctx, aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt}); refusal(t, err) != aisession.RefuseHomeUnsafe {
		t.Errorf("prepare: %v", err)
	}
	if _, err := r.m.Start(ctx, aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true}); refusal(t, err) != aisession.RefuseHomeUnsafe {
		t.Errorf("start: %v", err)
	}
	if list, _ := r.m.List(ctx); len(list) != 0 {
		t.Errorf("a refused start left %d sessions", len(list))
	}
}

// gitInit makes a repository in a fresh directory and returns its root.
func gitInit(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	return dir
}

func toolDir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "tool"), "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The session PATH drops every entry inside a worktree of the session's
// repository and of every repository the console serves (Options.WorkDirs),
// keeps the others, and a served repository that no longer exists does not
// make it drop everything.
func TestSessionPATHDropsWorktreeEntries(t *testing.T) {
	skipNotUnix(t)
	out := filepath.Join(t.TempDir(), "path.txt")
	dump := script(t, "pathdump", "printf '%s' \"$PATH\" > \"$1\"\n")
	served := gitInit(t)
	gone := filepath.Join(t.TempDir(), "gone", ".git")
	set := aisessiontest.Settings{
		Rules: map[string]config.AIRule{"pathdump": {Kind: config.AIRuleKindTerminal, Match: "pathdump", Decision: config.AIDecisionAllow}},
	}
	r := newRigWith(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "pathdump", "args": []string{out}}),
		call(acp.MethodTerminalWaitForExit, map[string]any{"sessionId": "$SESSION", "terminalId": "$TERMINAL"}),
	}}, set, func(o *aisession.Options) {
		o.WorkDirs = func(context.Context) ([]string, error) { return []string{filepath.Join(served, ".git"), gone}, nil }
	}, dump)
	inWorktree := toolDir(t, filepath.Join(r.wt, "bin"))
	inPrimary := toolDir(t, filepath.Join(r.primary, "tools"))
	inServed := toolDir(t, filepath.Join(served, "scripts"))
	keep, err := filepath.EvalSymlinks(toolDir(t, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", strings.Join([]string{inWorktree, inPrimary, inServed, keep, os.Getenv("PATH")}, string(os.PathListSeparator)))

	if s := r.run(t, person(context.Background()), "path"); s.Code != aisession.CodeEndTurn {
		t.Fatalf("session ended %s", s.Code)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	entries := filepath.SplitList(string(b))
	has := func(dir string) bool {
		for _, e := range entries {
			if e == dir {
				return true
			}
		}
		return false
	}
	for _, dir := range []string{inWorktree, inPrimary, inServed} {
		if has(dir) {
			t.Errorf("the session PATH kept %s: %v", dir, entries)
		}
	}
	if !has(keep) {
		t.Errorf("the session PATH dropped %s, which is in no repository: %v", keep, entries)
	}
}

// When the served repositories cannot be read, nothing starts.
func TestSessionRefusesUnreadableWorkDirs(t *testing.T) {
	skipNotUnix(t)
	r := newRigWith(t, aisessiontest.Script{}, aisessiontest.Settings{}, func(o *aisession.Options) {
		o.WorkDirs = func(context.Context) ([]string, error) { return nil, errors.New("registry is not private") }
	})
	ctx := person(context.Background())
	if _, err := r.m.Start(ctx, aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true}); refusal(t, err) != aisession.RefuseUnsafeEnvironment {
		t.Errorf("start: %v", err)
	}
}

// Past the bound on what the agent sends in session/update, the session
// ends limit_output.
func TestLimitOutputEndsTheSession(t *testing.T) {
	skipNotUnix(t)
	aisession.SetMaxUpdateBytesForTest(t, 4096)
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		{Update: aisessiontest.Message(strings.Repeat("x", 3000))},
		{Update: aisessiontest.Message(strings.Repeat("y", 3000))},
		{WaitCancel: true},
	}}, aisessiontest.Settings{})
	if s := r.run(t, person(context.Background()), "talk"); s.Code != aisession.CodeLimitOutput {
		t.Errorf("session ended %s, want %s", s.Code, aisession.CodeLimitOutput)
	}
}

func writeCall(path string) aisessiontest.Step {
	return asyncCall(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/" + path, "content": "x"})
}

// At most four asks wait per session: a fifth request is denied
// too_many_asks at once, without asking.
func TestAsksWaitingCap(t *testing.T) {
	skipNotUnix(t)
	steps := []aisessiontest.Step{}
	for i := range aisession.MaxAsksWaiting + 1 {
		steps = append(steps, writeCall("f"+string(rune('a'+i))+".txt"), aisessiontest.Step{Sleep: "50ms"})
	}
	steps = append(steps, aisessiontest.Step{WaitCancel: true})
	r := newRig(t, aisessiontest.Script{Steps: steps}, aisessiontest.Settings{})
	ctx := person(context.Background())
	s := r.start(t, ctx, "write")
	deadline := time.Now().Add(30 * time.Second)
	for {
		writes := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodWriteTextFile)
		if len(writes) == 1 {
			if writes[0].ErrorCode != acp.CodeDenied || writes[0].ErrorData != aisession.VerdictTooManyAsks {
				t.Fatalf("the fifth write = %+v", writes[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("writes answered: %+v", writes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, asks, _ := r.m.Get(ctx, s.ID); len(asks) != aisession.MaxAsksWaiting {
		t.Errorf("%d asks wait, want %d", len(asks), aisession.MaxAsksWaiting)
	}
	if _, err := r.m.Cancel(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if s = r.d.Wait(t, s.ID, 60*time.Second); s.Code != aisession.CodeCancelled {
		t.Errorf("session ended %s", s.Code)
	}
}

// At most 30 asks a minute per session: the 31st is denied rate without
// asking.
func TestAskRateCap(t *testing.T) {
	skipNotUnix(t)
	var steps []aisessiontest.Step
	for i := range aisession.MaxAsksPerMinute + 1 {
		steps = append(steps, call(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/r" + strings.Repeat("x", i%5) + ".txt", "content": "x"}))
	}
	r := newRig(t, aisessiontest.Script{Steps: steps}, aisessiontest.Settings{})
	ctx := person(context.Background())
	s := r.start(t, ctx, "write")
	// Reject every ask as it appears.
	var asked atomic.Int64
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			got, asks, err := r.m.Get(ctx, s.ID)
			if err != nil || got.State == aisession.StateEnded {
				return
			}
			for _, a := range asks {
				if _, err := r.m.Answer(ctx, aisession.AnswerRequest{Session: s.ID, Ask: a.ID, Decision: aisession.Deny}); err == nil {
					asked.Add(1)
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	s = r.d.Wait(t, s.ID, 120*time.Second)
	<-done
	writes := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodWriteTextFile)
	if len(writes) != aisession.MaxAsksPerMinute+1 {
		t.Fatalf("%d writes answered", len(writes))
	}
	for i, w := range writes {
		want := aisession.VerdictRejected
		if i == aisession.MaxAsksPerMinute {
			want = aisession.VerdictRate
		}
		if w.ErrorCode != acp.CodeDenied || w.ErrorData != want {
			t.Errorf("write %d = %+v, want %s", i, w, want)
		}
	}
	if asked.Load() != aisession.MaxAsksPerMinute || s.Counters.Asks != aisession.MaxAsksPerMinute {
		t.Errorf("asked %d, counted %d, want %d", asked.Load(), s.Counters.Asks, aisession.MaxAsksPerMinute)
	}
}

func terminalRule(name string) aisessiontest.Settings {
	return aisessiontest.Settings{Rules: map[string]config.AIRule{name: {Kind: config.AIRuleKindTerminal, Match: name, Decision: config.AIDecisionAllow}}}
}

func createTerminal(command string) aisessiontest.Step {
	return call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": command})
}

// At most four terminals run at once: a fifth is denied
// too_many_terminals.
func TestTerminalsAtOnceCap(t *testing.T) {
	skipNotUnix(t)
	bin := script(t, "sleeper", "exec /bin/sleep 30\n")
	var steps []aisessiontest.Step
	for range aisession.MaxTerminalsOpen + 1 {
		steps = append(steps, createTerminal("sleeper"))
	}
	r := newRig(t, aisessiontest.Script{Steps: steps}, terminalRule("sleeper"), bin)
	s := r.run(t, person(context.Background()), "terminals")
	creates := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodTerminalCreate)
	if len(creates) != aisession.MaxTerminalsOpen+1 {
		t.Fatalf("%d creates (session %s)", len(creates), s.Code)
	}
	for i, c := range creates {
		last := i == aisession.MaxTerminalsOpen
		if last && (c.ErrorCode != acp.CodeDenied || c.ErrorData != aisession.VerdictTooManyTerminals) || !last && c.ErrorCode != 0 {
			t.Errorf("create %d = %+v", i, c)
		}
	}
}

// At most 64 terminals per session, even when each has exited.
func TestTerminalsPerSessionCap(t *testing.T) {
	skipNotUnix(t)
	bin := script(t, "quick", "exit 0\n")
	var steps []aisessiontest.Step
	for range aisession.MaxTerminals {
		steps = append(steps, createTerminal("quick"),
			call(acp.MethodTerminalWaitForExit, map[string]any{"sessionId": "$SESSION", "terminalId": "$TERMINAL"}),
			call(acp.MethodTerminalRelease, map[string]any{"sessionId": "$SESSION", "terminalId": "$TERMINAL"}))
	}
	steps = append(steps, createTerminal("quick"))
	r := newRig(t, aisessiontest.Script{Steps: steps}, terminalRule("quick"), bin)
	s := r.run(t, person(context.Background()), "terminals")
	creates := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodTerminalCreate)
	if len(creates) != aisession.MaxTerminals+1 {
		t.Fatalf("%d creates (session %s)", len(creates), s.Code)
	}
	for i, c := range creates[:aisession.MaxTerminals] {
		if c.ErrorCode != 0 {
			t.Fatalf("create %d = %+v", i, c)
		}
	}
	if last := creates[aisession.MaxTerminals]; last.ErrorCode != acp.CodeDenied || last.ErrorData != aisession.VerdictTooManyTerminals {
		t.Errorf("create 65 = %+v", last)
	}
}

const previewMarker = "preview-marker-7f3a"

func askFiles(t *testing.T, r *rig, id aisession.ID) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.home, "ai", "sessions", string(id), "asks"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A write's preview lives only in its 0600 ask file, which is removed when
// the ask expires, while the session goes on. It waits the shortest ask
// timeout (30 s) for real: moving the clock further would make the host's
// heartbeat look lost.
func TestWritePreviewRemovedOnExpiry(t *testing.T) {
	skipNotUnix(t)
	if testing.Short() {
		t.Skip("waits 30 s for the ask to expire")
	}
	shortest := config.MinAIAskTimeout
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/late.txt", "content": previewMarker}),
		{WaitCancel: true},
	}}, aisessiontest.Settings{Sessions: config.UserAISessions{AskTimeout: &shortest}})
	ctx := person(context.Background())
	s := r.start(t, ctx, "write")
	a := r.waitAsk(t, s.ID)
	if a.Diff == nil || a.Diff.NewText != previewMarker {
		t.Fatalf("preview %+v", a.Diff)
	}
	info, err := os.Stat(filepath.Join(r.home, "ai", "sessions", string(s.ID), "asks", a.ID+".json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("ask file: %v %v", info, err)
	}
	deadline := time.Now().Add(shortest + 30*time.Second)
	for len(askFiles(t, r, s.ID)) != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if files := askFiles(t, r, s.ID); len(files) != 0 {
		t.Errorf("ask files after expiry: %v", files)
	}
	if got, _, _ := r.m.Get(ctx, s.ID); got.State == aisession.StateEnded {
		t.Fatalf("the session ended with the ask: %s", got.Code)
	}
	assertTreeLacks(t, r.home, previewMarker)
	if _, err := r.m.Cancel(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	r.d.Wait(t, s.ID, 60*time.Second)
}

// When the session ends with an ask waiting, the ask and its preview are
// gone with it.
func TestWritePreviewRemovedOnSessionEnd(t *testing.T) {
	skipNotUnix(t)
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		asyncCall(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/end.txt", "content": previewMarker}),
		{WaitCancel: true},
	}}, aisessiontest.Settings{})
	ctx := person(context.Background())
	s := r.start(t, ctx, "write")
	a := r.waitAsk(t, s.ID)
	if a.Diff == nil || a.Diff.NewText != previewMarker {
		t.Fatalf("preview %+v", a.Diff)
	}
	if _, err := r.m.Cancel(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	r.d.Wait(t, s.ID, 60*time.Second)
	if _, err := os.Stat(filepath.Join(r.home, "ai", "sessions", string(s.ID), "asks")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the asks folder outlived the session: %v (%v)", err, askFiles(t, r, s.ID))
	}
	assertTreeLacks(t, r.home, previewMarker)
}
