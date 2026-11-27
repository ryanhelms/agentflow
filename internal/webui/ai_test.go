package webui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/bus"
	"github.com/ryanhelms/agentflow/internal/command"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/policy"
)

type fakeRespondIn struct {
	Session     string `json:"session"`
	Ask         string `json:"ask"`
	Decision    string `json:"decision"`
	Fingerprint string `json:"fingerprint"`
}

type fakeCancelIn struct {
	Session string `json:"session"`
}

type fakeAICall struct {
	name      string
	respond   fakeRespondIn
	cancel    fakeCancelIn
	requester aisession.Requester
	client    string
}

// fakeAISpecs registers stand-ins for lane 7.3's ai.respond and ai.cancel
// in a test registry. With m set they answer through the Manager, as the
// real Specs do.
func fakeAISpecs(t *testing.T, m aisession.Manager) chan fakeAICall {
	t.Helper()
	reg := command.NewRegistry()
	calls := make(chan fakeAICall, 64)
	who := func(ctx context.Context) aisession.Requester { r, _ := aisession.RequesterFrom(ctx); return r }
	if err := reg.Add(command.Spec[fakeRespondIn, any]{Name: AIRespondCommand, Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in fakeRespondIn) (any, error) {
			calls <- fakeAICall{name: AIRespondCommand, respond: in, requester: who(ctx), client: a.Client}
			if m == nil {
				return struct{}{}, nil
			}
			d := aisession.Deny
			if in.Decision == "allow" {
				d = aisession.Allow
			}
			return m.Answer(ctx, aisession.AnswerRequest{Session: aisession.ID(in.Session), Ask: in.Ask, Decision: d, Fingerprint: in.Fingerprint})
		}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Add(command.Spec[fakeCancelIn, any]{Name: AICancelCommand, Policy: command.Policy{Class: policy.Read},
		Run: func(ctx context.Context, a *app.App, in fakeCancelIn) (any, error) {
			calls <- fakeAICall{name: AICancelCommand, cancel: in, requester: who(ctx), client: a.Client}
			if m == nil {
				return struct{}{}, nil
			}
			return m.Cancel(ctx, aisession.ID(in.Session))
		}}); err != nil {
		t.Fatal(err)
	}
	old := aiCommands
	aiCommands = func() *command.Registry { return reg }
	t.Cleanup(func() { aiCommands = old })
	return calls
}

func aiServer(dir string) *server {
	return &server{repo: &gitx.Repo{Dir: dir}, origin: "http://127.0.0.1:41511", instanceKey: randomKey(), session: randomKey(), rememberKey: randomKey()}
}

func TestAIRoutes(t *testing.T) {
	s := testServer(t)
	for _, path := range []string{"/api/ai/respond", "/api/ai/cancel"} {
		if w := harnessRequest(t, s, path, "{}", map[string]string{"Origin": "http://evil.invalid"}); w.Code != 403 {
			t.Errorf("%s from another origin: %d", path, w.Code)
		}
		if w := harnessRequest(t, s, path, "{}", map[string]string{"X-Agentflow-Request": ""}); w.Code != 403 {
			t.Errorf("%s without the request header: %d", path, w.Code)
		}
		if w := harnessRequest(t, s, path, "{}", map[string]string{"Authorization": ""}); w.Code != 401 {
			t.Errorf("%s without a session: %d", path, w.Code)
		}
	}
	sess := string(aisession.NewID())
	ask, fp := "ask_0123456789abcdef", strings.Repeat("ab", 32)
	good := `{"session":"` + sess + `","ask":"` + ask + `","decision":"allow","fingerprint":"` + fp + `"}`
	if w := harnessRequest(t, s, "/api/ai/respond", good, nil); w.Code != 503 {
		t.Errorf("without the commands: %d", w.Code)
	}
	calls := fakeAISpecs(t, nil)
	now := time.Now()
	old := aiNow
	aiNow = func() time.Time { return now }
	t.Cleanup(func() { aiNow = old })
	for _, body := range []string{
		`{"session":"x","ask":"` + ask + `","decision":"allow","fingerprint":"` + fp + `"}`,
		`{"session":"` + sess + `","ask":"ask_1","decision":"allow","fingerprint":"` + fp + `"}`,
		`{"session":"` + sess + `","ask":"` + ask + `","decision":"allow_always","fingerprint":"` + fp + `"}`,
		`{"session":"` + sess + `","ask":"` + ask + `","decision":"allow","fingerprint":"short"}`,
		`{"session":"` + sess + `","ask":"` + ask + `","decision":"allow","fingerprint":"` + fp + `","by":"agent"}`,
	} {
		if w := harnessRequest(t, s, "/api/ai/respond", body, nil); w.Code != 400 {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	w := harnessRequest(t, s, "/api/ai/respond", good, nil)
	if w.Code != 202 || w.Body.String() != "{}\n" {
		t.Fatalf("respond: %d %q", w.Code, w.Body)
	}
	c := <-calls
	want := fakeRespondIn{Session: sess, Ask: ask, Decision: "allow", Fingerprint: fp}
	if c.name != AIRespondCommand || c.respond != want || c.client != aiClient ||
		c.requester != (aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceConsole, Client: aiClient}) {
		t.Errorf("respond call %+v", c)
	}
	if w := harnessRequest(t, s, "/api/ai/cancel", `{"session":"`+sess+`"}`, nil); w.Code != 202 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	if c := <-calls; c.name != AICancelCommand || c.cancel.Session != sess || c.requester.Kind != aisession.RequesterPerson {
		t.Errorf("cancel call %+v", c)
	}
	// At most 30 a minute per console, both routes together.
	for i := 2; i < aiPerMinute; i++ {
		if w := harnessRequest(t, s, "/api/ai/cancel", `{"session":"`+sess+`"}`, nil); w.Code != 202 {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	if w := harnessRequest(t, s, "/api/ai/respond", good, nil); w.Code != 429 {
		t.Errorf("past the limit: %d", w.Code)
	}
	now = now.Add(time.Minute)
	if w := harnessRequest(t, s, "/api/ai/respond", good, nil); w.Code != 202 {
		t.Errorf("a minute later: %d", w.Code)
	}
}

// TestAIPanelFollowsSession: a daemon runtime with the session store and
// the fake agent shows the session, its ask and its end in the ai
// projection, and an allow from the console route reaches the host.
func TestAIPanelFollowsSession(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh script")
	}
	content := "hi from the agent\n"
	agent := aisessiontest.Install(t, "fake-agent", aisessiontest.Script{Steps: []aisessiontest.Step{
		{Update: aisessiontest.Message("working")},
		{Call: &aisessiontest.Call{Method: acp.MethodWriteTextFile, Params: aisessiontest.Params(map[string]any{
			"sessionId": "$SESSION", "path": "$CWD/src/hello.txt", "content": content})}},
	}})
	set := aisessiontest.Settings{}
	set.Overrides = map[string]harnessdef.Definition{"demo": {Name: "Demo", Command: "fake-agent"}}
	home := aisessiontest.Home(t, set, agent.Dir)
	aisessiontest.Ready(t, home, "demo")
	wt, _ := aisessiontest.Worktree(t)
	st, err := aisession.OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	oldOpts := newAIOptions
	newAIOptions = func() AIOptions { return AIOptions{Store: st} }
	t.Cleanup(func() { newAIOptions = oldOpts })

	ctx := context.Background()
	w, _, err := inspectWorktree(ctx, wt)
	if err != nil {
		t.Fatal(err)
	}
	rt, stop, err := startEventRuntime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	repo, err := rt.attach(w)
	if err != nil {
		t.Fatal(err)
	}
	l, unlisten := repo.projector.listen()
	defer unlisten()

	detach, d := aisessiontest.InProcessDetach(t)
	m := aisession.NewManager(aisession.Options{
		Home: home, Version: "test", Detach: detach,
		Emit: func(ctx context.Context, e bus.Event) error {
			_, err := rt.bus.Publish(ctx, e)
			return err
		},
		WorktreeID: func(context.Context, string) (string, error) { return w.ID, nil },
	})
	d.Use(m)
	fakeAISpecs(t, m)
	s := aiServer(wt)

	person := aisession.WithRequester(ctx, aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceTerminal, Client: "cli"})
	sess, err := m.Start(person, aisession.StartRequest{Harness: "demo", Prompt: "say hi", Dir: wt, Detach: true})
	if err != nil {
		t.Fatal(err)
	}
	if sess.Repo != string(repo.id) {
		t.Fatalf("the session's repository %q is not the daemon's %q", sess.Repo, repo.id)
	}

	// The panel shows the waiting ask with its preview.
	state := waitAIState(t, l, func(st AIState) bool {
		return len(st.Sessions) == 1 && len(st.Sessions[0].Asks) == 1 && st.Sessions[0].State == string(aisession.StateWaiting)
	})
	got := state.Sessions[0]
	ask := got.Asks[0]
	if got.ID != string(sess.ID) || got.Prompt != "say hi" || got.Harness != "demo" || got.State != string(aisession.StateWaiting) || state.Running != 1 {
		t.Errorf("session %+v (running %d)", got, state.Running)
	}
	raw, _ := json.Marshal(ask.Diff)
	if string(raw) != `{"path":"src/hello.txt","new_text":"hi from the agent\n"}` || len(ask.Paths) != 1 || ask.Paths[0] != ask.Diff.Path {
		t.Errorf("ask diff %s, paths %v", raw, ask.Paths)
	}
	if !ask.CanAllow || ask.Kind != string(aisession.KindWrite) || len(ask.Fingerprint) != 64 || !aiAskRule.MatchString(ask.ID) {
		t.Errorf("ask %+v", ask)
	}
	raw, _ = json.Marshal(got.Counters)
	for _, k := range []string{`"tool_calls"`, `"asks":1`, `"allowed"`, `"denied"`, `"unasked"`} {
		if !strings.Contains(string(raw), k) {
			t.Errorf("counters %s lack %s", raw, k)
		}
	}

	// The person allows it from the console.
	body, _ := json.Marshal(map[string]string{"session": string(sess.ID), "ask": ask.ID, "decision": "allow", "fingerprint": ask.Fingerprint})
	if rec := harnessRequest(t, s, "/api/ai/respond", string(body), nil); rec.Code != 202 {
		t.Fatalf("respond: %d %s", rec.Code, rec.Body)
	}
	state = waitAIState(t, l, func(st AIState) bool {
		return len(st.Sessions) == 1 && st.Sessions[0].State == string(aisession.StateEnded)
	})
	got = state.Sessions[0]
	if got.Code != aisession.CodeEndTurn || len(got.Asks) != 0 || state.Running != 0 || got.EndedAt == nil {
		t.Errorf("ended session %+v", got)
	}
	d.Wait(t, sess.ID, 30*time.Second)
	if b, _ := os.ReadFile(filepath.Join(wt, "src", "hello.txt")); string(b) != content {
		t.Errorf("hello.txt = %q", b)
	}
	var message string
	for _, it := range got.Items {
		if it.Kind == aisession.ItemMessage {
			message += it.Text
		}
	}
	if message != "working" {
		t.Errorf("items %+v", got.Items)
	}
}

func waitAIState(t *testing.T, l *listener, ok func(AIState) bool) AIState {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case up := <-l.ch:
			for _, s := range up.states {
				var st AIState
				if s.Projection == ProjectionAI && json.Unmarshal(s.State, &st) == nil && ok(st) {
					return st
				}
			}
		case <-deadline:
			t.Fatal("the ai projection never showed the expected state")
			return AIState{}
		}
	}
}
