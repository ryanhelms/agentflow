package aisession_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// draftRig is a session setup whose harness "drafter" is the phase 8 fake
// ACP agent (acptest) playing one draft mode, with its log.
type draftRig struct {
	home, wt, log string
	m             aisession.Manager
	d             *aisessiontest.Detacher
	events        chan bus.Event
	mu            sync.Mutex
	finished      []finishCall
}

type finishCall struct {
	id                          aisession.ID
	targets, stopReason, answer string
}

func newDraftRig(t *testing.T, script *acptest.SessionScript, sessions aisessiontest.Settings) *draftRig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh wrapper")
	}
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "fake.log")
	args := acptest.Install(t, bin, "fake-draft", acptest.Script{Session: script, Log: log})
	if sessions.Overrides == nil {
		sessions.Overrides = map[string]harnessdef.Definition{}
	}
	sessions.Overrides["drafter"] = harnessdef.Definition{Name: "Drafter", Command: "fake-draft", Args: args}
	home := aisessiontest.Home(t, sessions, bin)
	aisessiontest.Ready(t, home, "drafter")
	wt, _ := aisessiontest.Worktree(t)
	detach, d := aisessiontest.InProcessDetach(t)
	r := &draftRig{home: home, wt: wt, log: log, d: d, events: make(chan bus.Event, 1024)}
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
		FinishDraft: func(id aisession.ID, targets []byte, stopReason, answer string) []byte {
			r.mu.Lock()
			r.finished = append(r.finished, finishCall{id, string(targets), stopReason, answer})
			r.mu.Unlock()
			b, _ := json.Marshal(map[string]any{"session": id, "feature": "commit", "state": "ready", "candidates": []any{map[string]any{}}, "warnings": []any{}})
			return b
		},
	})
	d.Use(r.m)
	return r
}

const draftPrompt = "agentflow-draft v1 feature=commit\nthe frame\n<<<data abc kind=diff>>>\nsecret-free diff\n<<<end abc>>>\n"

func (r *draftRig) startDraft(t *testing.T, ctx context.Context) aisession.Session {
	t.Helper()
	s, err := r.m.Start(ctx, aisession.StartRequest{Harness: "drafter", Dir: r.wt, Detach: true, Draft: &aisession.DraftStart{
		Feature: "commit", Prompt: draftPrompt, Record: "manifest: feature commit, 1 part", Targets: []byte(`{"feature":"commit"}`), MaxDuration: time.Minute,
	}})
	if err != nil {
		t.Fatalf("start draft: %v", err)
	}
	return s
}

func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain=v1", "--untracked-files=all").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v %s", err, out)
	}
	return string(out)
}

func readLog(t *testing.T, path string) []acptest.LogEntry {
	t.Helper()
	entries, err := acptest.ReadLog(path)
	if err != nil {
		t.Fatal(err)
	}
	return entries
}

// TestDraftProfileDeniesEverything: with draft-hostile, every file,
// terminal and permission request is denied draft_profile, no ask file is
// written, the harness's own read tool call ends the draft draft_tool, the
// harness starts in an empty folder with no MCP server, and the worktree is
// unchanged.
func TestDraftProfileDeniesEverything(t *testing.T) {
	r := newDraftRig(t, acptest.DraftHostile(), aisessiontest.Settings{AgentStart: true})
	writeFile(t, filepath.Join(r.wt, "notes.txt"), "hello\n")
	before := gitStatus(t, r.wt)

	s := r.startDraft(t, person(context.Background()))
	if s.Kind != aisession.KindDraft || s.Feature != "commit" {
		t.Fatalf("record: kind %q feature %q", s.Kind, s.Feature)
	}
	dir := filepath.Join(r.home, "ai", "sessions", string(s.ID))
	asks := filepath.Join(dir, "asks")
	end := r.d.Wait(t, s.ID, 60*time.Second)
	if end.Code != aisession.CodeDraftTool {
		t.Fatalf("draft ended %q, want draft_tool", end.Code)
	}
	if end.Counters.Asks != 0 {
		t.Errorf("asks held: %d", end.Counters.Asks)
	}
	if entries, _ := os.ReadDir(asks); len(entries) != 0 {
		t.Errorf("ask files written: %v", entries)
	}
	if got := gitStatus(t, r.wt); got != before {
		t.Errorf("the worktree changed:\n%s\nwas:\n%s", got, before)
	}

	log := readLog(t, r.log)
	want := map[string]bool{acp.MethodReadTextFile: false, acp.MethodWriteTextFile: false, acp.MethodTerminalCreate: false}
	perms := 0
	for _, e := range log {
		switch e.Asked {
		case acp.MethodReadTextFile, acp.MethodWriteTextFile, acp.MethodTerminalCreate:
			want[e.Asked] = true
			if e.Code != acp.CodeDenied || e.ErrData != aisession.CodeDraftProfile {
				t.Errorf("%s answered %d %q, want -32001 draft_profile", e.Asked, e.Code, e.ErrData)
			}
		case acp.MethodRequestPermission:
			perms++
			var resp acp.RequestPermissionResponse
			if err := json.Unmarshal(e.Answer, &resp); err != nil || resp.Outcome.Outcome != acp.OutcomeSelected || resp.Outcome.OptionID != "reject-once" {
				t.Errorf("permission answered %s (%v), want reject-once", e.Answer, err)
			}
		}
	}
	for m, seen := range want {
		if !seen {
			t.Errorf("the fake never sent %s", m)
		}
	}
	if perms != 4 {
		t.Errorf("permission requests answered: %d, want 4", perms)
	}

	// The empty folder: the harness's working directory and ACP cwd are
	// <session>/work, never the worktree; no MCP server is passed.
	work := filepath.Join(dir, "work")
	if real, err := filepath.EvalSymlinks(filepath.Dir(work)); err == nil {
		work = filepath.Join(real, "work")
	}
	for _, e := range log {
		if e.Dir != "" && e.Dir != work {
			t.Errorf("the harness started in %s, want %s", e.Dir, work)
		}
		if e.Method == acp.MethodSessionNew {
			var req acp.NewSessionRequest
			_ = json.Unmarshal(e.Params, &req)
			if req.Cwd != work {
				t.Errorf("session/new cwd %s, want %s", req.Cwd, work)
			}
			if len(req.MCPServers) != 0 {
				t.Errorf("an MCP server was passed: %+v", req.MCPServers)
			}
		}
	}

	// The turn's files are gone; the result and the record are private.
	for _, name := range []string{"draft-prompt.txt", "work"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("%s outlived the draft", name)
		}
	}
	for _, name := range []string{"draft.json", "session.json", "draft-targets.json"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v %v", name, info, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.finished) != 1 || r.finished[0].stopReason != aisession.CodeDraftTool {
		t.Fatalf("FinishDraft calls: %+v", r.finished)
	}
}

// The draft's prompt goes to the harness and nowhere else: the transcript's
// prompt item is the manifest; the answer chunks are joined for the finish
// step, which runs before the session ends.
func TestDraftPromptAndFinish(t *testing.T) {
	r := newDraftRig(t, acptest.Draft(), aisessiontest.Settings{AgentStart: true})
	s := r.startDraft(t, agentCtx(context.Background()))
	end := r.d.Wait(t, s.ID, 60*time.Second)
	if end.Code != aisession.CodeEndTurn {
		t.Fatalf("draft ended %q", end.Code)
	}
	if got := acptest.Prompts(readLog(t, r.log)); len(got) != 1 || got[0] != draftPrompt {
		t.Fatalf("prompts sent: %q", got)
	}
	items, _, err := r.m.Items(context.Background(), s.ID, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if strings.Contains(it.Text, "secret-free diff") || strings.Contains(string(it.Data), "secret-free diff") {
			t.Errorf("the prompt reached the transcript: %+v", it)
		}
		// The draft's words stay out of the transcript: only draft.json
		// holds them, where Finish withholds a secret.
		if (it.Kind == aisession.ItemMessage || it.Kind == aisession.ItemThought) && !strings.HasSuffix(it.Text, "bytes: the draft's own text is in its result]") {
			t.Errorf("the draft's text reached the transcript: %+v", it)
		}
	}
	if items[0].Kind != aisession.ItemPrompt || items[0].Text != "manifest: feature commit, 1 part" {
		t.Errorf("prompt item: %+v", items[0])
	}
	r.mu.Lock()
	calls := slices.Clone(r.finished)
	r.mu.Unlock()
	if len(calls) != 1 || calls[0].targets != `{"feature":"commit"}` || calls[0].answer != acptest.DraftAnswer(acptest.DraftValid, draftPrompt) {
		t.Fatalf("FinishDraft: %+v", calls)
	}
	// draft.json, then ai.draft.finished, then ai.session.ended.
	sess, draft, err := r.m.Draft(context.Background(), s.ID)
	if err != nil || sess.ID != s.ID || !strings.Contains(string(draft), `"state": "ready"`) {
		t.Fatalf("Draft: %s %v", draft, err)
	}
	var order []bus.Type
	for _, e := range drainEvents(r.events) {
		order = append(order, e.Type)
		if e.Type == bus.TypeAIDraftFinished {
			var p bus.AIDraftFinished
			_ = json.Unmarshal(e.Payload, &p)
			if p.Session != string(s.ID) || p.State != "ready" || p.Candidates != 1 {
				t.Errorf("payload %+v", p)
			}
		}
		if e.Type == bus.TypeAISessionStarted && !strings.Contains(string(e.Payload), `"kind":"draft"`) {
			t.Errorf("ai.session.started without kind: %s", e.Payload)
		}
	}
	fi, ei := slices.Index(order, bus.TypeAIDraftFinished), slices.Index(order, bus.TypeAISessionEnded)
	if fi < 0 || ei < 0 || fi > ei {
		t.Errorf("event order %v", order)
	}
	// An ai run session is not a draft.
	if _, _, err := r.m.Draft(context.Background(), "ais_0123456789abcdef0123456789abcdef"); err == nil {
		t.Error("Draft of an unknown session")
	}
}

func drainEvents(ch chan bus.Event) []bus.Event {
	var out []bus.Event
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// Drafts run for the primary checkout too (owner decision 4), and the
// caps hold under concurrent starts: one running draft per worktree.
func TestDraftConcurrentStarts(t *testing.T) {
	r := newDraftRig(t, acptest.DraftHang(), aisessiontest.Settings{AgentStart: true})
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		ok   []aisession.ID
		errs []string
	)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "drafter", Dir: r.wt, Detach: true,
				Draft: &aisession.DraftStart{Feature: "commit", Prompt: draftPrompt, Record: "m", Targets: []byte("{}")}})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, refusal(t, err))
				return
			}
			ok = append(ok, s.ID)
		}()
	}
	wg.Wait()
	if len(ok) != 1 {
		t.Fatalf("%d drafts started in one worktree (refusals %v)", len(ok), errs)
	}
	for _, c := range errs {
		if c != aisession.RefuseLimitDraftWorktree {
			t.Errorf("refusal %q, want limit_draft_worktree", c)
		}
	}
	// An ai run session is counted apart: the worktree's session cap is free.
	if _, err := r.m.Prepare(person(context.Background()), aisession.StartRequest{Harness: "drafter", Prompt: "x", Dir: r.wt, Detach: true}); err != nil {
		t.Errorf("ai run beside a draft: %v", err)
	}
	if _, err := r.m.Cancel(person(context.Background()), ok[0]); err != nil {
		t.Fatal(err)
	}
	if end := r.d.Wait(t, ok[0], 60*time.Second); end.Code != aisession.CodeCancelled {
		t.Errorf("cancelled draft ended %q", end.Code)
	}
}

// A draft may run for the primary checkout; an ai run session may not.
func TestDraftPrimaryCheckout(t *testing.T) {
	r := newDraftRig(t, acptest.Draft(), aisessiontest.Settings{AgentStart: true})
	_, primary := aisessiontest.Worktree(t)
	req := aisession.StartRequest{Harness: "drafter", Dir: primary, Detach: true, Draft: &aisession.DraftStart{Feature: "commit", Prompt: draftPrompt, Record: "m", Targets: []byte("{}")}}
	plan, err := r.m.Prepare(person(context.Background()), req)
	if err != nil {
		t.Fatalf("draft in the primary checkout: %v", err)
	}
	if plan.MCP || plan.MCPSkipped != aisession.KindDraft || plan.Prompt != "" {
		t.Errorf("plan: mcp %v %q prompt %q", plan.MCP, plan.MCPSkipped, plan.Prompt)
	}
	req.Draft = nil
	req.Prompt = "x"
	if _, err := r.m.Prepare(person(context.Background()), req); refusal(t, err) != aisession.RefusePrimaryCheckout {
		t.Errorf("ai run in the primary checkout: %v", err)
	}
	s, err := r.m.Start(person(context.Background()), aisession.StartRequest{Harness: "drafter", Dir: primary, Detach: true,
		Draft: &aisession.DraftStart{Feature: "commit", Prompt: draftPrompt, Record: "m", Targets: []byte("{}")}})
	if err != nil {
		t.Fatal(err)
	}
	if end := r.d.Wait(t, s.ID, 60*time.Second); end.Code != aisession.CodeEndTurn {
		t.Errorf("draft in the primary checkout ended %q", end.Code)
	}
}

// The answer is capped: past 256 KiB of message text the draft is
// cancelled answer_too_large.
func TestDraftAnswerTooLarge(t *testing.T) {
	big := strings.Repeat("x", 64<<10)
	var steps []acptest.Step
	for range 5 {
		steps = append(steps, acptest.Step{Update: acptest.JSON(map[string]any{
			"sessionUpdate": acp.UpdateAgentMessageChunk, "content": map[string]any{"type": "text", "text": big},
		})})
	}
	steps = append(steps, acptest.Step{Sleep: "24h"})
	r := newDraftRig(t, &acptest.SessionScript{Steps: steps}, aisessiontest.Settings{AgentStart: true})
	s := r.startDraft(t, person(context.Background()))
	if end := r.d.Wait(t, s.ID, 60*time.Second); end.Code != aisession.CodeAnswerTooLarge {
		t.Fatalf("ended %q, want answer_too_large", end.Code)
	}
}

// TestDraftStopReasonWins: the harness ends its turn, or exits, right after
// the message that broke a draft rule. The rule's code is recorded while
// that message is handled, so it always wins over the turn's own end; a
// stop reason taken in the background would lose that race and end the
// draft end_turn or exited.
func TestDraftStopReasonWins(t *testing.T) {
	big := strings.Repeat("x", 64<<10)
	chunks := func() []acptest.Step {
		var steps []acptest.Step
		for range 5 {
			steps = append(steps, acptest.Step{Update: acptest.JSON(map[string]any{
				"sessionUpdate": acp.UpdateAgentMessageChunk, "content": map[string]any{"type": "text", "text": big},
			})})
		}
		return steps
	}
	tool := []acptest.Step{{ToolUnasked: acptest.JSON(map[string]any{"toolCallId": "own-read", "title": "Read", "kind": acp.ToolRead})}}
	for _, c := range []struct {
		name  string
		steps []acptest.Step
		want  string
	}{
		{"too large, then end_turn", append(chunks(), acptest.Step{Stop: acp.StopEndTurn}), aisession.CodeAnswerTooLarge},
		{"too large, then exit", append(chunks(), acptest.Step{Crash: true}), aisession.CodeAnswerTooLarge},
		{"tool, then end_turn", append(tool, acptest.Step{Stop: acp.StopEndTurn}), aisession.CodeDraftTool},
		{"tool, then exit", append(tool, acptest.Step{Crash: true}), aisession.CodeDraftTool},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newDraftRig(t, &acptest.SessionScript{Steps: c.steps}, aisessiontest.Settings{AgentStart: true})
			for range 3 {
				s := r.startDraft(t, person(context.Background()))
				if end := r.d.Wait(t, s.ID, 60*time.Second); end.Code != c.want {
					t.Fatalf("ended %q, want %s", end.Code, c.want)
				}
			}
		})
	}
}

// With an account layer (AGENTFLOW_HOME is another folder), a feature runs
// only when both layers name the same harness; the draft time is the lower.
func TestDraftAccountLayer(t *testing.T) {
	r := newDraftRig(t, acptest.Draft(), aisessiontest.Settings{AgentStart: true})
	acct := t.TempDir()
	write := func(harness string) {
		t.Helper()
		body := "ai:\n  sessions:\n    agent_start: true\n"
		if harness != "" {
			body += "  features:\n    commit:\n      harness: " + harness + "\n"
		}
		body += "  drafts:\n    max_duration: 45s\n"
		if err := os.WriteFile(filepath.Join(acct, "config.yml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	detach, _ := aisessiontest.InProcessDetach(t)
	m := aisession.NewManager(aisession.Options{Home: r.home, Version: "test", AccountHome: acct, Detach: detach})
	req := aisession.StartRequest{Harness: "drafter", Dir: r.wt, Detach: true, Draft: &aisession.DraftStart{Feature: "commit", Prompt: draftPrompt, Record: "m", Targets: []byte("{}"), MaxDuration: 10 * time.Minute}}
	for _, h := range []string{"", "other"} {
		write(h)
		if _, err := m.Prepare(person(context.Background()), req); refusal(t, err) != aisession.RefuseFeatureOffAccount {
			t.Errorf("account harness %q: %v", h, err)
		}
	}
	write("drafter")
	plan, err := m.Prepare(person(context.Background()), req)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Limits.MaxDuration != 45*time.Second || plan.Limits.IdleTimeout > 45*time.Second {
		t.Errorf("limits %+v, want the account's 45s", plan.Limits)
	}
}
