package ai_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ryanhelms/agentflow/internal/acp/acptest"
	"github.com/ryanhelms/agentflow/internal/ai"
	"github.com/ryanhelms/agentflow/internal/ai/aitest"
	"github.com/ryanhelms/agentflow/internal/aisession"
	"github.com/ryanhelms/agentflow/internal/aisession/aisessiontest"
	"github.com/ryanhelms/agentflow/internal/app"
	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/jsonrpc/jsonrpctest"
)

func TestMain(m *testing.M) {
	jsonrpctest.RunChild()
	os.Exit(m.Run())
}

// rig is a worktree, a home whose commit feature is routed to the fake
// agent "drafter" (acptest, with a log), an App on the worktree and
// Options that host drafts in the test process.
type rig struct {
	home, wt, log string
	a             *app.App
	o             aisession.Options
	d             *aisessiontest.Detacher
}

func newRig(t *testing.T, script *acptest.SessionScript, features config.UserAIFeatures) *rig {
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
	home := aisessiontest.Home(t, aisessiontest.Settings{AgentStart: true, Features: features,
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft", Args: args}}}, bin)
	aisessiontest.Ready(t, home, "drafter")
	wt, _ := aisessiontest.Worktree(t)
	t.Setenv("AGENTFLOW_HOME", home)
	a, err := app.Open(context.Background(), wt, "cli")
	if err != nil {
		t.Fatal(err)
	}
	detach, d := aisessiontest.InProcessDetach(t)
	o := aisession.Options{Home: home, Version: "test", Detach: detach, FinishDraft: ai.Finish,
		WorktreeID: func(context.Context, string) (string, error) { return "wt-test", nil }}
	d.Use(aisession.NewManager(o))
	return &rig{home: home, wt: wt, log: log, a: a, o: o, d: d}
}

func commitOn() config.UserAIFeatures {
	return config.UserAIFeatures{Commit: config.UserAIFeature{Harness: "drafter"}}
}

func person() context.Context {
	return aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceTerminal, Client: "cli"})
}

func code(t *testing.T, err error) string {
	t.Helper()
	var r *aisession.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a refusal", err)
	}
	return r.Code
}

// stagedDiff is a Gather that reads the staged diff, as the commit feature
// will (lane 8.2): the framework, not the feature, filters it.
func stagedDiff(ctx context.Context, a *app.App, _ ai.Request) ([]ai.Part, error) {
	out, err := a.Repo.Git(ctx, "diff", "--cached", "--no-color", "--no-ext-diff", "--no-textconv")
	if err != nil {
		return nil, err
	}
	return []ai.Part{{Kind: ai.PartDiff, Label: "staged changes", Source: "git", Text: out}}, nil
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.invalid"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDraftPromptIsData: what the fake receives holds the fences and the
// nonce, the diff of notes.txt, hidden characters as <U+XXXX>; and not the
// secret file's content, the worktree's or the home's absolute path, an
// environment value or a planted token.
func TestDraftPromptIsData(t *testing.T) {
	r := newRig(t, acptest.Draft(), commitOn())
	f := aitest.Use(t, &aitest.Feature{GatherFn: stagedDiff})
	t.Setenv("DRAFT_TEST_ENV", "env-value-never-sent")
	token := "gh" + "p_" + strings.Repeat("Z", 36)
	write(t, filepath.Join(r.wt, "notes.txt"), "hello\u202e world\npath "+r.wt+"/x and "+os.Getenv("HOME")+"/y\n"+token+"\n")
	write(t, filepath.Join(r.wt, ".env"), "API_KEY=placeholder-value-never-sent\n")
	write(t, filepath.Join(r.wt, "go.sum"), "example.com/m v1 h1:generated-never-sent\n")
	git(t, r.wt, "add", "notes.txt", ".env", "go.sum")
	before, _ := exec.Command("git", "-C", r.wt, "status", "--porcelain").Output()

	p, err := ai.Prepare(person(), r.a, r.o, ai.Request{Feature: ai.FeatureCommit, Hint: "keep it short"})
	if err != nil {
		t.Fatal(err)
	}
	if f.Gathered != 1 {
		t.Fatalf("gathered %d times", f.Gathered)
	}
	m := p.Manifest
	if len(m.Parts) != 2 || m.Parts[0].Kind != ai.PartDiff || m.Parts[1].Kind != ai.PartHint {
		t.Fatalf("parts %+v", m.Parts)
	}
	ex := map[string]string{}
	for _, e := range m.Parts[0].Excluded {
		ex[e.Path] = e.Reason
	}
	if ex[".env"] != ai.ExcludedSecretFile || ex["go.sum"] != ai.ExcludedGenerated {
		t.Errorf("exclusions %+v", m.Parts[0].Excluded)
	}
	if m.Parts[0].Redacted != 1 {
		t.Errorf("redacted %d, want 1", m.Parts[0].Redacted)
	}
	if len(m.NeverSent) == 0 || m.PromptSHA256 == "" || m.Bytes != len(p.Prompt()) || m.Harness != "drafter" {
		t.Errorf("manifest %+v", m)
	}
	// Nothing ran: no session directory.
	if _, err := os.Stat(filepath.Join(r.home, "ai", "sessions")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(r.home, "ai", "sessions"))
		if len(entries) != 0 {
			t.Fatalf("Prepare made sessions: %v", entries)
		}
	}

	s, err := ai.Start(person(), r.a, p, r.o, true)
	if err != nil {
		t.Fatal(err)
	}
	end := r.d.Wait(t, s.ID, 60*time.Second)
	if end.Code != aisession.CodeEndTurn {
		t.Fatalf("draft ended %s", end.Code)
	}
	entries, err := acptest.ReadLog(r.log)
	if err != nil {
		t.Fatal(err)
	}
	prompts := acptest.Prompts(entries)
	if len(prompts) != 1 {
		t.Fatalf("prompts %d", len(prompts))
	}
	sent := prompts[0]
	if !strings.HasPrefix(sent, "agentflow-draft v1 feature=commit\n") {
		t.Errorf("first line: %q", strings.SplitN(sent, "\n", 2)[0])
	}
	if !strings.Contains(sent, "<<<data ") || !strings.Contains(sent, "<<<end ") || !strings.Contains(sent, "+hello<U+202E> world") {
		t.Errorf("fences or the visible hidden character are missing:\n%s", sent)
	}
	for _, bad := range []string{"placeholder-value-never-sent", "generated-never-sent", "env-value-never-sent", token, r.wt, r.home, os.Getenv("HOME"), "\u202e"} {
		if bad != "" && strings.Contains(sent, bad) {
			t.Errorf("the prompt holds %q", bad)
		}
	}
	if !strings.Contains(sent, "path ./x and ~/y") {
		t.Errorf("paths were not written as . and ~:\n%s", sent)
	}
	after, _ := exec.Command("git", "-C", r.wt, "status", "--porcelain").Output()
	if string(after) != string(before) {
		t.Errorf("the worktree changed:\n%s\n%s", before, after)
	}
	// The host finished the draft with ai.Finish: ready, one commit.prepare
	// candidate that is only an input, nothing applied.
	_, raw, err := aisession.NewManager(r.o).Draft(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	var d ai.Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.State != ai.DraftReady || len(d.Candidates) != 1 || d.Candidates[0].Operation != "commit.prepare" || !strings.HasPrefix(d.Note, "AI-drafted text from Drafter.") {
		t.Fatalf("draft %+v", d)
	}
	out, _ := exec.Command("git", "-C", r.wt, "log", "--oneline").Output()
	if strings.Count(string(out), "\n") != 1 {
		t.Errorf("a commit was made:\n%s", out)
	}
}

// TestDraftRefusesSecretContext: a secret the filter cannot redact refuses
// the draft before anything starts, naming the part, never the match.
func TestDraftRefusesSecretContext(t *testing.T) {
	r := newRig(t, acptest.Draft(), commitOn())
	aitest.Use(t, &aitest.Feature{GatherFn: stagedDiff})
	// A shape the redaction does not know (split: not a real key).
	secret := "AI" + "za" + "QWERTY" + strings.Repeat("x", 29)
	write(t, filepath.Join(r.wt, "config.txt"), "maps = "+secret+"\n")
	git(t, r.wt, "add", "config.txt")
	_, err := ai.Prepare(person(), r.a, r.o, ai.Request{Feature: ai.FeatureCommit})
	if code(t, err) != ai.CodeSecretInContext {
		t.Fatalf("refusal %v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "QWERTY") || !strings.Contains(err.Error(), "staged changes") {
		t.Errorf("refusal text %q", err.Error())
	}
	if _, err := os.Stat(filepath.Join(r.home, "ai", "sessions")); err == nil {
		if entries, _ := os.ReadDir(filepath.Join(r.home, "ai", "sessions")); len(entries) != 0 {
			t.Error("a session was made")
		}
	}
}

// The checks of ai.draft run in order and refuse with their codes before
// anything is gathered or sent.
func TestDraftRefusals(t *testing.T) {
	r := newRig(t, acptest.Draft(), config.UserAIFeatures{})
	f := aitest.Use(t, &aitest.Feature{})
	cases := []struct {
		name string
		ctx  context.Context
		req  ai.Request
		want string
	}{
		{"unknown feature", person(), ai.Request{Feature: "review"}, ai.CodeUnknownFeature},
		{"another feature's input", person(), ai.Request{Feature: ai.FeatureCommit, Thread: "PRRT_1"}, ai.CodeUsage},
		{"a hint past the limit", person(), ai.Request{Feature: ai.FeatureCommit, Hint: strings.Repeat("h", ai.MaxHint+1)}, ai.CodeUsage},
		{"feature off", person(), ai.Request{Feature: ai.FeatureCommit}, ai.CodeFeatureOff},
		{"not registered", person(), ai.Request{Feature: ai.FeaturePR}, ai.CodeUnknownFeature},
		{"a session", aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterSession}), ai.Request{Feature: ai.FeatureCommit}, aisession.RefuseNested},
	}
	for _, c := range cases {
		if _, err := ai.Prepare(c.ctx, r.a, r.o, c.req); code(t, err) != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
		}
	}
	if f.Gathered != 0 {
		t.Errorf("gathered %d times before the checks passed", f.Gathered)
	}
	// An agent while agent_start is off.
	aisessiontest.Write(t, r.home, aisessiontest.Settings{AgentStart: false, Features: commitOn(),
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft"}}})
	a, err := app.Open(context.Background(), r.wt, "claude")
	if err != nil {
		t.Fatal(err)
	}
	agent := aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterAgent, Surface: aisession.SurfaceMCP})
	if _, err := ai.Prepare(agent, a, r.o, ai.Request{Feature: ai.FeatureCommit}); code(t, err) != aisession.RefuseAgentStartOff {
		t.Errorf("agent_start off: %v", err)
	}
	// The feature's own refusal passes through with its code and fix.
	f.Refuse = &aisession.Refusal{Code: "nothing_to_draft", Message: "Nothing is staged.", Fix: "Stage a change."}
	if _, err := ai.Prepare(person(), a, r.o, ai.Request{Feature: ai.FeatureCommit}); code(t, err) != "nothing_to_draft" {
		t.Errorf("gather refusal: %v", err)
	}
}

// The manifest is the same for the same context, so a confirmation can be
// redeemed; a changed context changes its hash.
func TestManifestStable(t *testing.T) {
	r := newRig(t, acptest.Draft(), commitOn())
	aitest.Use(t, &aitest.Feature{GatherFn: stagedDiff})
	write(t, filepath.Join(r.wt, "notes.txt"), "one\n")
	git(t, r.wt, "add", "notes.txt")
	p1, err := ai.Prepare(person(), r.a, r.o, ai.Request{Feature: ai.FeatureCommit})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := ai.Prepare(person(), r.a, r.o, ai.Request{Feature: ai.FeatureCommit})
	if err != nil {
		t.Fatal(err)
	}
	if p1.Prompt() == p2.Prompt() {
		t.Error("the nonce did not change between builds")
	}
	b1, _ := json.Marshal(p1.Manifest)
	b2, _ := json.Marshal(p2.Manifest)
	if string(b1) != string(b2) {
		t.Errorf("manifests differ for the same context:\n%s\n%s", b1, b2)
	}
	write(t, filepath.Join(r.wt, "notes.txt"), "two\n")
	git(t, r.wt, "add", "notes.txt")
	p3, err := ai.Prepare(person(), r.a, r.o, ai.Request{Feature: ai.FeatureCommit})
	if err != nil {
		t.Fatal(err)
	}
	if p3.Manifest.PromptSHA256 == p1.Manifest.PromptSHA256 {
		t.Error("a changed diff kept the prompt hash")
	}
}

// Every registered feature has its ai.features.<id>.harness field, and
// every FeatureID constant has one. Lanes 8.2 and 8.3 register the
// features themselves, so a constant without a registered feature is
// reported here until they land (see the lane 8.1 report).
func TestEveryFeatureHasAHarnessField(t *testing.T) {
	var cfg config.UserAIFeatures
	ids := cfg.IDs()
	for _, id := range ai.IDs() {
		found := false
		for _, c := range ids {
			if c == string(id) {
				found = true
			}
		}
		if !found {
			t.Errorf("feature %s has no ai.features.%s.harness field", id, id)
		}
		if !cfg.SetHarness(string(id), "x") {
			t.Errorf("feature %s cannot be routed", id)
		}
	}
	if len(ids) != len(ai.IDs()) {
		t.Errorf("config features %v, ai features %v", ids, ai.IDs())
	}
	var missing []string
	for _, id := range ai.IDs() {
		if _, ok := ai.Lookup(id); !ok {
			missing = append(missing, string(id))
		}
	}
	for _, f := range ai.Features() {
		if !ai.Known(f.ID()) {
			t.Errorf("registered feature %s is not a FeatureID constant", f.ID())
		}
	}
	if len(missing) > 0 {
		t.Skipf("not registered yet (lanes 8.2 and 8.3): %v", missing)
	}
}

// TestManifestShowsAllSent: the manifest shows the caller's hint and a
// ticket's description in full, exactly as the prompt carries them, at
// every size the request allows.
func TestManifestShowsAllSent(t *testing.T) {
	r := newRig(t, acptest.Draft(), config.UserAIFeatures{Ticket: config.UserAIFeature{Harness: "drafter"}})
	aitest.Use(t, &aitest.Feature{FeatureID: ai.FeatureTicket, GatherFn: func(_ context.Context, _ *app.App, req ai.Request) ([]ai.Part, error) {
		return []ai.Part{{Kind: ai.PartText, Label: "your description", Source: "caller", Text: req.Text}}, nil
	}})
	text := strings.Repeat("words of the description ", (ai.MaxText-64)/25) + "TAIL_OF_TEXT"
	hint := strings.Repeat("h", ai.MaxHint-16) + "TAIL_OF_HINT"
	p, err := ai.Prepare(person(), r.a, r.o, ai.Request{Feature: ai.FeatureTicket, Text: text, Hint: hint})
	if err != nil {
		t.Fatal(err)
	}
	m := p.Manifest
	if !strings.HasSuffix(m.Text, "TAIL_OF_TEXT") || !strings.Contains(p.Prompt(), m.Text) {
		t.Errorf("manifest text %d bytes of %d sent; tail shown: %v", len(m.Text), len(text), strings.HasSuffix(m.Text, "TAIL_OF_TEXT"))
	}
	if !strings.HasSuffix(m.Hint, "TAIL_OF_HINT") || !strings.Contains(p.Prompt(), m.Hint) {
		t.Errorf("manifest hint %d bytes of %d", len(m.Hint), len(hint))
	}
}
