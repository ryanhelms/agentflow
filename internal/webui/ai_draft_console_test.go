package webui_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/ai/aitest"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/webui"
)

// TestDraftConsoleFlow: the console offers the feature, keeps the confirm
// token server-side, gives the page the manifest only, starts the draft
// for its own worktree after the person's Send, and the result arrives
// over the stream as a draft, never among the sessions.
func TestDraftConsoleFlow(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh wrapper")
	}
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	args := acptest.Install(t, bin, "fake-draft", acptest.Script{Session: acptest.Draft()})
	features := config.UserAIFeatures{Commit: config.UserAIFeature{Harness: "drafter"}}
	home := aisessiontest.Home(t, aisessiontest.Settings{AgentStart: true, Features: features,
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft", Args: args}}}, bin)
	aisessiontest.Ready(t, home, "drafter")
	wt, _ := aisessiontest.Worktree(t)
	st, err := aisession.OpenStore(home)
	if err != nil {
		t.Fatal(err)
	}
	user := &config.User{}
	user.AI.Features = features
	webui.SetAIFeatureStateForTest(t, user, map[string]harness.PublishedState{"drafter": {State: harness.StateNative}})
	rt := webui.StartAIRuntimeForTest(t, st, wt)
	detach, d := aisessiontest.InProcessDetach(t)
	oldOptions := ops.AIOptions
	ops.AIOptions = func(a *app.App) aisession.Options {
		o := ops.AISessionOptions(a)
		o.Detach, o.Emit = detach, rt.Emit
		o.WorktreeID = func(context.Context, string) (string, error) { return rt.WorktreeID, nil }
		return o
	}
	t.Cleanup(func() { ops.AIOptions = oldOptions })
	d.Use(aisession.NewManager(aisession.Options{
		Home: home, Version: "test", Detach: detach, Emit: rt.Emit, FinishDraft: ai.Finish,
		WorktreeID: func(context.Context, string) (string, error) { return rt.WorktreeID, nil },
	}))
	aitest.Use(t, &aitest.Feature{GatherFn: func(ctx context.Context, a *app.App, _ ai.Request) ([]ai.Part, error) {
		out, err := a.Repo.Git(ctx, "diff", "--cached", "--no-color")
		return []ai.Part{{Kind: ai.PartDiff, Label: "staged changes", Source: "git", Text: out}}, err
	}})
	stageFile(t, wt, "notes.txt", "secret-free notes\n")
	console := webui.NewAIConsoleForTest(wt)

	// The page names no path, harness or token.
	for _, body := range []string{
		`{"feature":"commit","input":{"confirm_token":"x"}}`,
		`{"feature":"commit","input":{"dry_run":true}}`,
		`{"feature":"commit","input":{},"harness":"other"}`,
		`{"feature":"commit","input":{"dir":"/tmp"}}`,
		`{"feature":"push","input":{}}`,
	} {
		if rec := console.Post(t, "/api/ai/draft", body, nil); rec.Code != 400 {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body)
		}
	}
	rec := console.Post(t, "/api/ai/draft", `{"feature":"commit","input":{"hint":"short"}}`, nil)
	if rec.Code != 200 {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	var got struct {
		Confirm  string      `json:"confirm"`
		Manifest ai.Manifest `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || !strings.HasPrefix(got.Confirm, "cf_") || got.Manifest.Feature != ai.FeatureCommit || got.Manifest.Harness != "drafter" {
		t.Fatalf("answer %s", body)
	}
	for _, bad := range []string{"confirm_token", "secret-free notes", "<<<data", "op_"} {
		if strings.Contains(body, bad) {
			t.Errorf("the page got %q: %s", bad, body)
		}
	}
	if list, _ := st.List(); len(list) != 0 {
		t.Fatalf("a draft started before Send: %+v", list)
	}
	if rec := console.Post(t, "/api/ai/draft/confirm", `{"confirm":"`+got.Confirm+`"}`, nil); rec.Code != 202 || rec.Body.String() != "{}\n" {
		t.Fatalf("confirm: %d %s", rec.Code, rec.Body)
	}
	state := rt.WaitState(t, func(s webui.AIState) bool {
		return len(s.Drafts) == 1 && s.Drafts[0].Draft != nil
	})
	dr := state.Drafts[0]
	if len(state.Sessions) != 0 || dr.State != string(ai.DraftReady) || dr.Feature != "commit" || dr.StartedBy != aisession.RequesterPerson ||
		dr.Manifest == nil || dr.Manifest.PromptSHA256 != got.Manifest.PromptSHA256 || len(dr.Draft.Candidates) != 1 {
		t.Fatalf("state %+v / draft %+v", state, dr)
	}
	if len(state.Features) != 1 || !state.Features[0].Available || state.Features[0].ID != "commit" {
		t.Errorf("features %+v", state.Features)
	}
	// A confirmation is used once.
	if rec := console.Post(t, "/api/ai/draft/confirm", `{"confirm":"`+got.Confirm+`"}`, nil); rec.Code != 409 {
		t.Errorf("reused confirm: %d %s", rec.Code, rec.Body)
	}
	// A changed context after the confirmation is refused, and nothing starts.
	rec = console.Post(t, "/api/ai/draft", `{"feature":"commit","input":{}}`, nil)
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	stageFile(t, wt, "notes.txt", "changed notes\n")
	if rec := console.Post(t, "/api/ai/draft/confirm", `{"confirm":"`+got.Confirm+`"}`, nil); rec.Code != 409 || !strings.Contains(rec.Body.String(), "operation_changed") {
		t.Errorf("changed context: %d %s", rec.Code, rec.Body)
	}
	if list, _ := st.List(); len(list) != 1 {
		t.Errorf("sessions %d, want the one draft", len(list))
	}
	// Six draft requests a minute per console.
	for i := range 10 {
		rec := console.Post(t, "/api/ai/draft", `{"feature":"commit","input":{}}`, nil)
		if rec.Code == 429 {
			if i == 0 {
				t.Error("limited at once")
			}
			return
		}
	}
	t.Error("no rate limit")
}

func stageFile(t *testing.T, wt, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(gitPath, "-C", wt, "add", name).CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
}

var gitPath = func() string {
	p, err := exec.LookPath("git")
	if err != nil {
		return "git"
	}
	return p
}()
