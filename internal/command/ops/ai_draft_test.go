package ops_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/ai/aitest"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/command/ops"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
)

// Draft is the fake Manager's draft lookup: it has none.
func (f *fakeManager) Draft(ctx context.Context, id aisession.ID) (aisession.Session, []byte, error) {
	f.record(ctx)
	return aisession.Session{}, nil, aisession.ErrNoSession
}

// draftOps is a worktree with a staged change, a home whose commit feature
// is routed to the fake agent "drafter", the real Manager hosting detached
// drafts in the test process, and the aitest feature reading the staged
// diff.
type draftOps struct {
	home, wt, log string
	a             *app.App
	d             *aisessiontest.Detacher
}

func newDraftOps(t *testing.T, autonomy config.Autonomy) *draftOps {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh wrapper")
	}
	resetFake(t)
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "fake.log")
	args := acptest.Install(t, bin, "fake-draft", acptest.Script{Session: acptest.Draft(), Log: log})
	home := aisessiontest.Home(t, aisessiontest.Settings{AgentStart: true, Autonomy: autonomy,
		Features:  config.UserAIFeatures{Commit: config.UserAIFeature{Harness: "drafter"}},
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft", Args: args}}}, bin)
	aisessiontest.Ready(t, home, "drafter")
	wt, _ := aisessiontest.Worktree(t)
	detach, d := aisessiontest.InProcessDetach(t)
	oldOptions := ops.AIOptions
	ops.AIOptions = func(a *app.App) aisession.Options {
		o := ops.AISessionOptions(a)
		o.Detach = detach
		return o
	}
	t.Cleanup(func() { ops.AIOptions = oldOptions })
	old := app.NewAIManager
	app.NewAIManager = aisession.NewManager
	t.Cleanup(func() { app.NewAIManager = old })
	aitest.Use(t, &aitest.Feature{GatherFn: func(ctx context.Context, a *app.App, _ ai.Request) ([]ai.Part, error) {
		out, err := a.Repo.Git(ctx, "diff", "--cached", "--no-color")
		return []ai.Part{{Kind: ai.PartDiff, Label: "staged changes", Source: "git", Text: out}}, err
	}})
	stage(t, wt, "notes.txt", "hello\n")
	a, err := app.Open(t.Context(), wt, "claude")
	if err != nil {
		t.Fatal(err)
	}
	d.Use(aisession.NewManager(ops.AIOptions(a)))
	return &draftOps{home: home, wt: wt, log: log, a: a, d: d}
}

func stage(t *testing.T, wt, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(wt, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", wt, "add", name).CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
}

func (r *draftOps) sessions(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.home, "ai", "sessions"))
	if err != nil {
		return 0
	}
	return len(entries)
}

func status(t *testing.T, wt string) string {
	t.Helper()
	out, _ := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	return string(out)
}

// An agent's draft always needs the person's confirmation, under every
// autonomy level; the dry run and the first call start nothing; the
// confirmed call returns a draft whose candidate is only an input.
func TestDraftToolNeedsConfirmation(t *testing.T) {
	r := newDraftOps(t, config.Autonomy{Local: config.Auto, Remote: config.Auto, Actions: map[string]config.Level{ops.ActionDraft: config.Auto}})
	before := status(t, r.wt)
	cs, _ := aiTool(t, r.a, "agentflow_ai_draft")

	text, isErr := callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "commit", "dry_run": true})
	var m ai.Manifest
	if isErr || json.Unmarshal([]byte(text), &m) != nil || m.Feature != ai.FeatureCommit || m.PromptSHA256 == "" {
		t.Fatalf("dry run: %s", text)
	}
	if strings.Contains(text, "hello") {
		t.Errorf("the manifest holds the diff: %s", text)
	}
	text, isErr = callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "commit"})
	if isErr || !strings.Contains(text, `"needs_confirmation"`) {
		t.Fatalf("first call: %s", text)
	}
	var pending struct {
		Token   string      `json:"confirm_token"`
		Summary ai.Manifest `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text), &pending); err != nil || pending.Token == "" || pending.Summary.PromptSHA256 != m.PromptSHA256 {
		t.Fatalf("pending: %s", text)
	}
	if n := r.sessions(t); n != 0 {
		t.Fatalf("%d sessions before the confirmation", n)
	}
	text, isErr = callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "commit", "confirm_token": pending.Token})
	var d ai.Draft
	if isErr || json.Unmarshal([]byte(text), &d) != nil || d.State != ai.DraftReady || len(d.Candidates) != 1 || d.Candidates[0].Tool != "agentflow_commit_prepare" {
		t.Fatalf("confirmed call: %s", text)
	}
	if strings.Contains(text, "confirm_token") {
		t.Errorf("a candidate carries a confirm token: %s", text)
	}
	if got := status(t, r.wt); got != before {
		t.Errorf("the draft changed the worktree:\n%s", got)
	}
	// ai status SESSION shows the draft.
	cs2, _ := aiTool(t, r.a, "agentflow_ai_status")
	text, _ = callText(t, cs2, "agentflow_ai_status", map[string]any{"session": string(d.Session)})
	var st struct {
		Draft *ai.Draft `json:"draft"`
	}
	if json.Unmarshal([]byte(text), &st) != nil || st.Draft == nil || st.Draft.State != ai.DraftReady {
		t.Errorf("ai status: %s", text)
	}
}

// TestDraftConfirmBindsPrompt: a changed diff after the confirmation makes
// the token call fail operation_changed, and nothing starts.
func TestDraftConfirmBindsPrompt(t *testing.T) {
	r := newDraftOps(t, config.Autonomy{Local: config.Confirm, Remote: config.Confirm})
	cs, _ := aiTool(t, r.a, "agentflow_ai_draft")
	text, _ := callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "commit"})
	var pending struct {
		Token string `json:"confirm_token"`
	}
	if json.Unmarshal([]byte(text), &pending) != nil || pending.Token == "" {
		t.Fatalf("first call: %s", text)
	}
	stage(t, r.wt, "notes.txt", "hello, changed\n")
	text, isErr := callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "commit", "confirm_token": pending.Token})
	if !isErr || !strings.Contains(text, "changed") {
		t.Fatalf("a changed context was accepted: %s", text)
	}
	if n := r.sessions(t); n != 0 {
		t.Fatalf("%d sessions started", n)
	}
}

// The tool's input names no harness, directory, worktree, environment,
// limit or requester; a feature with no harness is refused feature_off.
func TestDraftToolInputs(t *testing.T) {
	r := newDraftOps(t, config.Autonomy{})
	_, spec := aiTool(t, r.a, "agentflow_ai_draft")
	raw, _ := json.Marshal(spec.InputSchema)
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	var got []string
	for k := range schema.Properties {
		got = append(got, k)
	}
	slices.Sort(got)
	want := []string{"confirm_token", "dry_run", "feature", "hint", "key", "link_to", "paths", "project", "template", "template_revision", "text", "thread", "type"}
	if !slices.Equal(got, want) {
		t.Errorf("properties %v, want %v", got, want)
	}
	cs, _ := aiTool(t, r.a, "agentflow_ai_draft")
	if text, isErr := callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "pr"}); !isErr || !strings.Contains(text, "unknown_feature") && !strings.Contains(text, "feature_off") {
		t.Errorf("pr: %s", text)
	}
	aitest.Use(t, &aitest.Feature{FeatureID: ai.FeaturePR})
	if text, isErr := callText(t, cs, "agentflow_ai_draft", map[string]any{"feature": "pr"}); !isErr || !strings.Contains(text, "(feature_off)") {
		t.Errorf("pr with a feature but no harness: %s", text)
	}
	if n := r.sessions(t); n != 0 {
		t.Errorf("%d sessions", n)
	}
	_ = time.Second
}
