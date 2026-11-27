package aisession_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

var forbiddenEnv = []string{
	"GH_TOKEN", "GITHUB_TOKEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "AWS_SECRET_ACCESS_KEY",
	"SSH_AUTH_SOCK", "CLAUDE_CODE_SESSION_ID", "AGENTFLOW_HOME", "HTTPS_PROXY", "AGENTFLOW_DEBUG",
}

func refusal(t *testing.T, err error) string {
	t.Helper()
	var r *aisession.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a refusal", err)
	}
	return r.Code
}

func TestSessionEnvironment(t *testing.T) {
	out := filepath.Join(t.TempDir(), "names.txt")
	bin := script(t, "envnames", "/usr/bin/env | /usr/bin/cut -d= -f1 > \"$1\"\n")
	set := aisessiontest.Settings{
		Overrides: map[string]harnessdef.Definition{"demo": {Name: "Demo", Command: "fake-agent", CredentialEnv: []string{"FAKE_AGENT_KEY"}}},
		Rules:     map[string]config.AIRule{"envnames": {Kind: config.AIRuleKindTerminal, Match: "envnames", Decision: config.AIDecisionAllow}},
	}
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "envnames", "args": []string{out}}),
		call(acp.MethodTerminalWaitForExit, map[string]any{"sessionId": "$SESSION", "terminalId": "$TERMINAL"}),
	}}, set, bin)
	for _, n := range forbiddenEnv {
		t.Setenv(n, "secret-"+strings.ToLower(n))
	}
	t.Setenv("FAKE_AGENT_KEY", "credential-value")

	s := r.run(t, person(context.Background()), "env")
	if s.Code != aisession.CodeEndTurn {
		t.Fatalf("session ended %s", s.Code)
	}
	log := aisessiontest.ReadLog(t, r.agent.Log)
	if len(log) == 0 || log[0].Env == nil {
		t.Fatal("the fake agent logged no environment")
	}
	harnessEnv := log[0].Env
	for _, n := range forbiddenEnv {
		if slices.Contains(harnessEnv, n) || log[0].EnvSet[n] {
			t.Errorf("%s reached the harness", n)
		}
	}
	if !log[0].EnvSet["FAKE_AGENT_KEY"] {
		t.Error("the definition's credential_env name did not reach the harness")
	}
	for _, n := range []string{"PATH", "HOME", aisession.EnvSession, "GIT_TERMINAL_PROMPT"} {
		if !slices.Contains(harnessEnv, n) {
			t.Errorf("the harness lacks %s: %v", n, harnessEnv)
		}
	}
	// Exactly the listed names ("Phase 7 session environment").
	listed := []string{"PATH", "HOME", "USER", "LOGNAME", "LANG", "TZ", "TERM", "TMPDIR", "SHELL",
		"GIT_TERMINAL_PROMPT", aisession.EnvSession, "FAKE_AGENT_KEY", "PWD", "SHLVL", "_"}
	for _, n := range harnessEnv {
		if !slices.Contains(listed, n) && !strings.HasPrefix(n, "LC_") && !strings.HasPrefix(n, "XDG_") {
			t.Errorf("the harness got %s, which is not a listed name", n)
		}
	}
	waits := aisessiontest.Calls(log, acp.MethodTerminalWaitForExit)
	if len(waits) != 1 || waits[0].ErrorCode != 0 {
		t.Fatalf("terminal wait = %+v", waits)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	termEnv := strings.Fields(string(b))
	for _, n := range append([]string{"FAKE_AGENT_KEY"}, forbiddenEnv...) {
		if slices.Contains(termEnv, n) {
			t.Errorf("%s reached the terminal", n)
		}
	}
	if !slices.Contains(termEnv, aisession.EnvSession) {
		t.Errorf("the terminal lacks %s: %v", aisession.EnvSession, termEnv)
	}
	// Nothing in the store holds a value.
	assertTreeLacks(t, r.home, "credential-value", "secret-gh_token", "secret-anthropic_api_key")
}

func TestSessionGuardRefusesRealPATH(t *testing.T) {
	set := aisessiontest.Settings{
		Rules: map[string]config.AIRule{"git": {Kind: config.AIRuleKindTerminal, Match: "git", Decision: config.AIDecisionAllow}},
	}
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "git", "args": []string{"status"}}),
	}}, set)
	ctx := person(context.Background())
	// A terminal whose program resolves outside the temporary directory
	// (git, through harnesstest's tools folder: a link to the real git)
	// is denied test_guard, whatever the rule says.
	s := r.run(t, ctx, "git")
	creates := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodTerminalCreate)
	if len(creates) != 1 || creates[0].ErrorCode != acp.CodeDenied || creates[0].ErrorData != aisession.VerdictTestGuard {
		t.Errorf("terminal git = %+v (session %s)", creates, s.Code)
	}
	// A harness found outside the temporary directory is never started.
	set.Overrides = map[string]harnessdef.Definition{
		"demo": {Name: "Demo", Command: "fake-agent"},
		"real": {Name: "Real", Command: "git"},
	}
	set.AgentStart = true
	aisessiontest.Write(t, r.home, set)
	before, _ := r.m.List(ctx)
	for _, detach := range []bool{true, false} {
		_, err := r.m.Start(ctx, aisession.StartRequest{Harness: "real", Prompt: "x", Dir: r.wt, Detach: detach})
		if code := refusal(t, err); code != aisession.RefuseTestGuard {
			t.Errorf("start real (detach %v) = %s", detach, code)
		}
	}
	if after, _ := r.m.List(ctx); len(after) != len(before) {
		t.Errorf("a refused start left a session: %+v", after)
	}
}

func TestAnswerRules(t *testing.T) {
	// The token shape is redacted everywhere else; the preview shows the
	// exact bytes.
	token := "ghp_" + strings.Repeat("Z", 36)
	content := "package src\n\n// exact bytes " + token + "\r\n"
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/src/forged.go", "content": "forged"}),
		call(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/src/new.go", "content": content}),
	}}, aisessiontest.Settings{})
	ctx := person(context.Background())
	s := r.start(t, ctx, "write")

	// 1. A forged allow written straight into the store, without the MAC.
	a := r.waitAsk(t, s.ID)
	forged, _ := json.Marshal(aisession.AnswerRecord{Ask: a.ID, Decision: aisession.Allow, By: aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceTerminal}, At: time.Now().UTC(), Fingerprint: a.Fingerprint})
	if err := os.WriteFile(filepath.Join(r.home, "ai", "sessions", string(s.ID), "asks", a.ID+".answer.json"), forged, 0o600); err != nil {
		t.Fatal(err)
	}

	// 2. The real write: who may answer it.
	b := r.waitAskOtherThan(t, s.ID, a.ID)
	if b.NewSHA256 != sha(content) || b.Diff == nil || b.Diff.NewText != content || b.Diff.OldText != "" || b.Diff.Cut || !b.CanAllow {
		t.Errorf("the preview = %+v %+v", b, b.Diff)
	}
	answer := func(ctx context.Context, d aisession.Decision, fp string) error {
		_, err := r.m.Answer(ctx, aisession.AnswerRequest{Session: s.ID, Ask: b.ID, Decision: d, Fingerprint: fp})
		return err
	}
	if code := refusal(t, answer(agentCtx(context.Background()), aisession.Allow, b.Fingerprint)); code != aisession.AnswerNeedsPerson {
		t.Errorf("an agent's allow = %s", code)
	}
	self := aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterSession, Session: s.ID, Client: aisession.SessionClientPrefix + string(s.ID)})
	if code := refusal(t, answer(self, aisession.Allow, b.Fingerprint)); code != aisession.AnswerSelf {
		t.Errorf("the session's own allow = %s", code)
	}
	lying := aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceMCP, Client: aisession.SessionClientPrefix + string(s.ID)})
	if code := refusal(t, answer(lying, aisession.Allow, b.Fingerprint)); code != aisession.AnswerSelf {
		t.Errorf("the session's client claiming a person = %s", code)
	}
	if code := refusal(t, answer(ctx, aisession.Allow, strings.Repeat("0", 64))); code != aisession.AnswerFingerprint {
		t.Errorf("a stale fingerprint = %s", code)
	}
	if err := answer(ctx, aisession.Allow, b.Fingerprint); err != nil {
		t.Fatalf("a person's allow: %v", err)
	}
	if code := refusal(t, answer(ctx, aisession.Deny, b.Fingerprint)); code != aisession.AnswerAnswered && code != aisession.AnswerNotFound {
		t.Errorf("a second answer = %s", code)
	}
	s = r.d.Wait(t, s.ID, 60*time.Second)

	writes := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodWriteTextFile)
	if len(writes) != 2 {
		t.Fatalf("writes = %+v", writes)
	}
	if writes[0].ErrorCode != acp.CodeDenied || writes[0].ErrorData != aisession.VerdictRejected {
		t.Errorf("the forged allow = %+v", writes[0])
	}
	if _, err := os.Stat(filepath.Join(r.wt, "src", "forged.go")); err == nil {
		t.Error("the forged allow wrote the file")
	}
	if writes[1].ErrorCode != 0 {
		t.Errorf("the allowed write = %+v", writes[1])
	}
	if got, _ := os.ReadFile(filepath.Join(r.wt, "src", "new.go")); string(got) != content {
		t.Errorf("written %q, want exactly %q", got, content)
	}
	// The preview is stored only in the ask file, removed once answered.
	if left, _ := os.ReadDir(filepath.Join(r.home, "ai", "sessions", string(s.ID), "asks")); len(left) != 0 {
		t.Errorf("ask files left: %v", left)
	}
	for _, it := range r.items(t, s.ID) {
		if strings.Contains(string(it.Data), "exact bytes") || strings.Contains(it.Text, "exact bytes") {
			t.Errorf("the transcript holds the write's content: %+v", it)
		}
	}
	for _, e := range r.drain() {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "exact bytes") {
			t.Errorf("an event holds the write's content: %s", b)
		}
	}
	// Nothing but the worktree file holds the content: no transcript,
	// audit line or session file in the home.
	assertTreeLacks(t, r.home, "exact bytes", token)
}

// waitAskOtherThan waits for a waiting ask of id other than not.
func (r *rig) waitAskOtherThan(t *testing.T, id aisession.ID, not string) aisession.AskRecord {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if _, asks, err := r.m.Get(person(context.Background()), id); err == nil {
			for _, a := range asks {
				if a.ID != not {
					return a
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %s held no second ask", id)
	return aisession.AskRecord{}
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestWritePreviewChanged(t *testing.T) {
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/README.md", "content": "agent\n"}),
	}}, aisessiontest.Settings{})
	ctx := person(context.Background())
	s := r.start(t, ctx, "write")
	a := r.waitAsk(t, s.ID)
	if a.Diff == nil || a.Diff.OldText != "hello\n" || a.Diff.NewText != "agent\n" || a.Diff.Path != "README.md" {
		t.Errorf("preview = %+v", a.Diff)
	}
	// The file changes after the person saw the preview.
	writeFile(t, filepath.Join(r.wt, "README.md"), "person\n")
	if _, err := r.m.Answer(ctx, aisession.AnswerRequest{Session: s.ID, Ask: a.ID, Decision: aisession.Allow, Fingerprint: a.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	r.d.Wait(t, s.ID, 60*time.Second)
	writes := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodWriteTextFile)
	if len(writes) != 1 || writes[0].ErrorCode != acp.CodeDenied || writes[0].ErrorData != aisession.VerdictChanged {
		t.Errorf("write = %+v", writes)
	}
	if b, _ := os.ReadFile(filepath.Join(r.wt, "README.md")); string(b) != "person\n" {
		t.Errorf("README.md = %q", b)
	}
}

func TestWritePreviewTooLarge(t *testing.T) {
	big := strings.Repeat("a", aisession.MaxPreview+1)
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/big.txt", "content": big}),
		call(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/bin.dat", "content": "text"}),
	}}, aisessiontest.Settings{})
	if err := os.WriteFile(filepath.Join(r.wt, "bin.dat"), []byte{0xff, 0xfe, 'x'}, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := person(context.Background())
	s := r.start(t, ctx, "write")
	for i, name := range []string{"big.txt", "bin.dat"} {
		a := r.waitAsk(t, s.ID)
		if a.CanAllow || a.Reason != aisession.ReasonPreviewTooLarge || a.Diff == nil || !a.Diff.Cut || a.Diff.OldText != "" || a.Diff.NewText != "" {
			t.Errorf("%s: ask = %+v %+v", name, a, a.Diff)
		}
		_, err := r.m.Answer(ctx, aisession.AnswerRequest{Session: s.ID, Ask: a.ID, Decision: aisession.Allow, Fingerprint: a.Fingerprint})
		if code := refusal(t, err); code != aisession.AnswerCannotAllow {
			t.Errorf("%s: allow = %s", name, code)
		}
		if _, err := r.m.Answer(ctx, aisession.AnswerRequest{Session: s.ID, Ask: a.ID, Decision: aisession.Deny}); err != nil {
			t.Fatalf("%s: reject: %v", name, err)
		}
		_ = i
		r.waitNoAsk(t, s.ID, a.ID)
	}
	r.d.Wait(t, s.ID, 60*time.Second)
	if _, err := os.Stat(filepath.Join(r.wt, "big.txt")); err == nil {
		t.Error("big.txt was written")
	}
}

// waitNoAsk waits until ask is no longer waiting.
func (r *rig) waitNoAsk(t *testing.T, id aisession.ID, ask string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		_, asks, err := r.m.Get(person(context.Background()), id)
		if err != nil || !slices.ContainsFunc(asks, func(a aisession.AskRecord) bool { return a.ID == ask }) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("ask %s still waits", ask)
}

func TestAskTimeoutDenies(t *testing.T) {
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	r := newRigWith(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		call(acp.MethodWriteTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/late.txt", "content": "x"}),
	}}, aisessiontest.Settings{}, func(o *aisession.Options) { o.Now = now })
	s := r.start(t, person(context.Background()), "write")
	r.waitAsk(t, s.ID)
	offset.Store(int64(config.MaxAIAskTimeout + time.Minute))
	s = r.d.Wait(t, s.ID, 60*time.Second)
	writes := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodWriteTextFile)
	if len(writes) != 1 || writes[0].ErrorCode != acp.CodeDenied || writes[0].ErrorData != aisession.VerdictExpired {
		t.Errorf("write = %+v", writes)
	}
	if !hasAnswer(r.items(t, s.ID), aisession.HandlerTimeout) {
		t.Error("the transcript shows no timeout answer")
	}
}

func hasAnswer(items []aisession.Item, handler string) bool {
	for _, it := range items {
		if it.Kind != aisession.ItemAnswer {
			continue
		}
		var d map[string]string
		if json.Unmarshal(it.Data, &d) == nil && d["handler"] == handler {
			return true
		}
	}
	return false
}

func TestStoreModesAndTranscripts(t *testing.T) {
	steps := []aisessiontest.Step{{Update: aisessiontest.Message("hi")}}
	r := newRig(t, aisessiontest.Script{Steps: steps}, aisessiontest.Settings{})
	s := r.run(t, person(context.Background()), "hello")
	dir := filepath.Join(r.home, "ai", "sessions", string(s.ID))
	for _, d := range []string{filepath.Join(r.home, "ai"), filepath.Join(r.home, "ai", "sessions"), dir} {
		if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
			t.Errorf("%s: %v %v", d, info.Mode(), err)
		}
	}
	err := filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, _ := e.Info()
		want := os.FileMode(0o600)
		if e.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s is %v, want %v", p, info.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "transcript.jsonl")); err != nil {
		t.Errorf("transcript: %v", err)
	}

	off := false
	r2 := newRig(t, aisessiontest.Script{Steps: steps}, aisessiontest.Settings{Sessions: config.UserAISessions{KeepTranscripts: &off}})
	s2 := r2.run(t, person(context.Background()), "hello")
	if _, err := os.Stat(filepath.Join(r2.home, "ai", "sessions", string(s2.ID), "transcript.jsonl")); err == nil {
		t.Error("keep_transcripts off kept the transcript")
	}
}

func TestRunningCapHoldsUnderRace(t *testing.T) {
	one := 1
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{{WaitCancel: true}}}, aisessiontest.Settings{Sessions: config.UserAISessions{MaxRunning: &one}})
	ctx := person(context.Background())
	// One worktree each, so only the running cap can refuse.
	dirs := make([]string, 8)
	for i := range dirs {
		dirs[i], _ = aisessiontest.Worktree(t)
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		started []aisession.ID
		codes   []string
	)
	for _, dir := range dirs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := r.m.Start(ctx, aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: dir, Detach: true})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				var ref *aisession.Refusal
				if errors.As(err, &ref) {
					codes = append(codes, ref.Code)
				} else {
					codes = append(codes, err.Error())
				}
				return
			}
			started = append(started, s.ID)
		}()
	}
	wg.Wait()
	if len(started) != 1 {
		t.Errorf("%d sessions started, want 1", len(started))
	}
	for _, c := range codes {
		if c != aisession.RefuseLimitRunning && c != aisession.RefuseLimitRate {
			t.Errorf("refused %s", c)
		}
	}
	for _, id := range started {
		_, _ = r.m.Cancel(ctx, id)
		r.d.Wait(t, id, 60*time.Second)
	}
}

func TestNoSessionStartsASession(t *testing.T) {
	r := newRig(t, aisessiontest.Script{}, aisessiontest.Settings{})
	// A caller inside a session, whatever requester it claims.
	t.Setenv(aisession.EnvSession, string(aisession.NewID()))
	_, err := r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true})
	if code := refusal(t, err); code != aisession.RefuseNested {
		t.Errorf("start inside a session = %s", code)
	}
	os.Unsetenv(aisession.EnvSession)
	self := aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterSession, Session: aisession.NewID()})
	_, err = r.m.Start(self, aisession.StartRequest{Harness: "demo", Prompt: "x", Dir: r.wt, Detach: true})
	if code := refusal(t, err); code != aisession.RefuseNested {
		t.Errorf("start by a session requester = %s", code)
	}
}

// TestEventsAndAuditCarryNoSessionText: no event payload or audit line
// holds the prompt, agent text, a command line, env values or an absolute
// path.
func TestEventsAndAuditCarryNoSessionText(t *testing.T) {
	bin := script(t, "lister", "exit 0\n")
	set := aisessiontest.Settings{Rules: map[string]config.AIRule{"lister": {Kind: config.AIRuleKindTerminal, Match: "lister", Decision: config.AIDecisionAllow}}}
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		{Update: aisessiontest.Message("AGENT-TEXT-1")},
		call(acp.MethodReadTextFile, map[string]any{"sessionId": "$SESSION", "path": "$CWD/README.md"}),
		call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "lister", "args": []string{"ARG-TEXT-1"}, "env": []map[string]string{{"name": "X", "value": "ENV-VALUE-1"}}}),
		call(acp.MethodTerminalWaitForExit, map[string]any{"sessionId": "$SESSION", "terminalId": "$TERMINAL"}),
	}}, set, bin)
	s := r.run(t, person(context.Background()), "PROMPT-TEXT-1")
	if s.Code != aisession.CodeEndTurn {
		t.Fatalf("session ended %s", s.Code)
	}
	events := r.drain()
	if len(events) == 0 {
		t.Fatal("no events")
	}
	secret := []string{"PROMPT-TEXT-1", "AGENT-TEXT-1", "ARG-TEXT-1", "ENV-VALUE-1", "hello\n", r.wt, r.home}
	for _, e := range events {
		b, _ := json.Marshal(e)
		for _, sec := range secret {
			if strings.Contains(string(b), sec) {
				t.Errorf("event %s holds %q: %s", e.Type, sec, b)
			}
		}
	}
	// The audit lines (outside the session store).
	var audit []string
	_ = filepath.WalkDir(r.home, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.Contains(e.Name(), "audit") {
			audit = append(audit, p)
		}
		return nil
	})
	if len(audit) == 0 {
		t.Fatal("no audit file")
	}
	for _, p := range audit {
		b, _ := os.ReadFile(p)
		for _, sec := range secret {
			if strings.Contains(string(b), sec) {
				t.Errorf("%s holds %q", p, sec)
			}
		}
		if !strings.Contains(string(b), string(s.ID)) {
			t.Errorf("%s has no line for the session", p)
		}
	}
}

// assertTreeLacks fails when any file under dir contains one of the values.
func assertTreeLacks(t *testing.T, dir string, values ...string) {
	t.Helper()
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, v := range values {
			if strings.Contains(string(b), v) {
				t.Errorf("%s holds %q", p, v)
			}
		}
		return nil
	})
}

func TestChunksJoinWithoutSeparator(t *testing.T) {
	thought := func(text string) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": text}})
		return b
	}
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		{Update: thought("thin")}, {Update: thought("king")},
		{Update: aisessiontest.Message("wor")}, {Update: aisessiontest.Message("ds\n")}, {Update: aisessiontest.Message("two")},
	}}, aisessiontest.Settings{})
	s := r.run(t, person(context.Background()), "x")
	got := map[string]string{}
	for _, it := range r.items(t, s.ID) {
		if it.Kind == aisession.ItemMessage || it.Kind == aisession.ItemThought {
			got[it.Kind] += it.Text
		}
	}
	if got[aisession.ItemThought] != "thinking" || got[aisession.ItemMessage] != "words\ntwo" {
		t.Errorf("joined = %q", got)
	}
}

func TestLimitDurationEndsTheSession(t *testing.T) {
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	r := newRigWith(t, aisessiontest.Script{Steps: []aisessiontest.Step{{WaitCancel: true}}}, aisessiontest.Settings{},
		func(o *aisession.Options) { o.Now = now })
	s := r.start(t, person(context.Background()), "wait")
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, _, err := r.m.Get(person(context.Background()), s.ID)
		if err == nil && got.State == aisession.StateRunning {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
	offset.Store(int64(config.MaxAIMaxDuration + time.Minute))
	if s = r.d.Wait(t, s.ID, 60*time.Second); s.Code != aisession.CodeLimitDuration {
		t.Errorf("session ended %s", s.Code)
	}
}

func TestLimitIdleEndsTheSession(t *testing.T) {
	var offset atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	r := newRigWith(t, aisessiontest.Script{Steps: []aisessiontest.Step{{WaitCancel: true}}}, aisessiontest.Settings{},
		func(o *aisession.Options) { o.Now = now })
	s := r.start(t, person(context.Background()), "wait")
	waitRunning(t, r, s.ID)
	// Past the idle timeout, inside the duration limit.
	offset.Store(int64(config.DefaultAIIdleTimeout + time.Minute))
	if s = r.d.Wait(t, s.ID, 60*time.Second); s.Code != aisession.CodeLimitIdle {
		t.Errorf("session ended %s", s.Code)
	}
}

func waitRunning(t *testing.T, r *rig, id aisession.ID) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		got, _, err := r.m.Get(person(context.Background()), id)
		if err == nil && got.State == aisession.StateRunning {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the session never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestSessionDeniesAgentflowHome: the host's own wiring of the session
// never-list. A person's rule allows cat; cat of the agentflow home, the
// session store or the console registry is still denied agentflow_home.
func TestSessionDeniesAgentflowHome(t *testing.T) {
	bin := script(t, "cat", "exit 0\n")
	set := aisessiontest.Settings{Rules: map[string]config.AIRule{"cat": {Kind: config.AIRuleKindTerminal, Match: "cat", Decision: config.AIDecisionAllow}}}
	create := func(args ...string) aisessiontest.Step {
		return call(acp.MethodTerminalCreate, map[string]any{"sessionId": "$SESSION", "command": "cat", "args": args})
	}
	r := newRig(t, aisessiontest.Script{Steps: []aisessiontest.Step{
		create("$AGENTFLOW_HOME/ai/sessions/ais_0/answer.key"),
		create("${AGENTFLOW_HOME}/config.yml"),
		create("~/.agentflow-ui/registry.json"),
		create("README.md"),
	}}, set, bin)
	r.run(t, person(context.Background()), "cat")
	creates := aisessiontest.Calls(aisessiontest.ReadLog(t, r.agent.Log), acp.MethodTerminalCreate)
	if len(creates) != 4 {
		t.Fatalf("creates = %+v", creates)
	}
	for i, c := range creates[:3] {
		if c.ErrorCode != acp.CodeDenied || c.ErrorData != aisession.VerdictAgentflowHome {
			t.Errorf("create %d = %+v", i, c)
		}
	}
	if creates[3].ErrorCode != 0 {
		t.Errorf("cat README.md = %+v", creates[3])
	}
}
