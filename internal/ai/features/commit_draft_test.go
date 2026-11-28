package features_test

import (
	"context"
	"encoding/json"
	"errors"
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
	_ "github.com/ryanhelms/agentflow/internal/ai/features"
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

// rig is a worktree on branch "task", a home whose commit, pr and ac
// features are routed to the fake agent "drafter", and Options that host
// drafts in the test process.
type rig struct {
	home, wt, log string
	o             aisession.Options
	d             *aisessiontest.Detacher
}

func newRig(t *testing.T) *rig {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake agent is a /bin/sh wrapper")
	}
	bin, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "fake.log")
	args := acptest.Install(t, bin, "fake-draft", acptest.Script{Session: acptest.Draft(), Log: log})
	on := config.UserAIFeature{Harness: "drafter"}
	home := aisessiontest.Home(t, aisessiontest.Settings{AgentStart: true,
		Features:  config.UserAIFeatures{Commit: on, PR: on, AC: on},
		Overrides: map[string]harnessdef.Definition{"drafter": {Name: "Drafter", Command: "fake-draft", Args: args}}}, bin)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	aisessiontest.Ready(t, home, "drafter")
	wt, _ := aisessiontest.Worktree(t)
	t.Setenv("AGENTFLOW_HOME", home)
	git(t, wt, "config", "user.name", "Dev")
	git(t, wt, "config", "user.email", "dev@example.invalid")
	detach, d := aisessiontest.InProcessDetach(t)
	o := aisession.Options{Home: home, Version: "test", Detach: detach, FinishDraft: ai.Finish,
		WorktreeID: func(context.Context, string) (string, error) { return "wt-test", nil }}
	d.Use(aisession.NewManager(o))
	return &rig{home: home, wt: wt, log: log, o: o, d: d}
}

func (r *rig) app(t *testing.T) *app.App {
	t.Helper()
	a, err := app.Open(context.Background(), r.wt, "cli")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (r *rig) prepare(t *testing.T, req ai.Request) (ai.Prepared, error) {
	t.Helper()
	return ai.Prepare(person(), r.app(t), r.o, req)
}

func (r *rig) mustPrepare(t *testing.T, req ai.Request) ai.Prepared {
	t.Helper()
	p, err := r.prepare(t, req)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// sameManifest checks that gathering again gives the same manifest, which
// the confirmation binds.
func (r *rig) sameManifest(t *testing.T, p ai.Prepared, req ai.Request) {
	t.Helper()
	first, _ := json.Marshal(p.Manifest)
	second, _ := json.Marshal(r.mustPrepare(t, req).Manifest)
	if string(first) != string(second) {
		t.Errorf("the same context gave another manifest:\n%s\n%s", first, second)
	}
}

// draft runs the prepared draft against the fake agent and returns it.
func (r *rig) draft(t *testing.T, p ai.Prepared) ai.Draft {
	t.Helper()
	s, err := ai.Start(person(), r.app(t), p, r.o, true)
	if err != nil {
		t.Fatal(err)
	}
	r.d.Wait(t, s.ID, 60*time.Second)
	_, raw, err := aisession.NewManager(r.o).Draft(context.Background(), s.ID)
	if err != nil {
		t.Fatal(err)
	}
	var d ai.Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func person() context.Context {
	return aisession.WithRequester(context.Background(), aisession.Requester{Kind: aisession.RequesterPerson, Surface: aisession.SurfaceTerminal, Client: "cli"})
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, root, name, s string) {
	t.Helper()
	p := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func refusal(t *testing.T, err error) string {
	t.Helper()
	var r *aisession.Refusal
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a refusal", err)
	}
	return r.Code
}

func kinds(m ai.Manifest) []ai.PartKind {
	var out []ai.PartKind
	for _, p := range m.Parts {
		out = append(out, p.Kind)
	}
	return out
}

func part(t *testing.T, m ai.Manifest, k ai.PartKind) ai.Part {
	t.Helper()
	for _, p := range m.Parts {
		if p.Kind == k {
			return p
		}
	}
	t.Fatalf("no %s part in %v", k, kinds(m))
	return ai.Part{}
}

func TestCommitDraft(t *testing.T) {
	r := newRig(t)
	if _, err := r.prepare(t, ai.Request{Feature: ai.FeatureCommit}); refusal(t, err) != "nothing_to_draft" {
		t.Fatalf("nothing staged: %v", err)
	}
	if _, err := r.prepare(t, ai.Request{Feature: ai.FeatureCommit, Paths: []string{"README.md"}}); refusal(t, err) != "nothing_to_draft" {
		t.Fatalf("an unchanged path: %v", err)
	}
	write(t, r.wt, "notes.txt", "hello\n")
	write(t, r.wt, "other.txt", "other-content\n")
	write(t, r.wt, ".env.example", "API_KEY=never-sent-value\n")
	git(t, r.wt, "add", "notes.txt", ".env.example")
	before := git(t, r.wt, "status", "--porcelain")

	p := r.mustPrepare(t, ai.Request{Feature: ai.FeatureCommit})
	m := p.Manifest
	want := []ai.PartKind{ai.PartFiles, ai.PartDiffStat, ai.PartDiff, ai.PartRules, ai.PartCommits}
	if !slices.Equal(kinds(m), want) {
		t.Errorf("parts %v, want %v", kinds(m), want)
	}
	diff := part(t, m, ai.PartDiff)
	if len(diff.Excluded) != 1 || diff.Excluded[0] != (ai.Exclusion{Path: ".env.example", Reason: ai.ExcludedSecretFile}) {
		t.Errorf("exclusions %+v", diff.Excluded)
	}
	prompt := p.Prompt()
	for _, s := range []string{"+hello", "A notes.txt", "Add AI attribution", "init"} {
		if s == "Add AI attribution" {
			s = "AI attribution"
		}
		if !strings.Contains(prompt, s) {
			t.Errorf("the prompt does not hold %q:\n%s", s, prompt)
		}
	}
	for _, s := range []string{"never-sent-value", "other-content", "dev@example.invalid"} {
		if strings.Contains(prompt, s) {
			t.Errorf("the prompt holds %q", s)
		}
	}
	if got := r.mustPrepare(t, ai.Request{Feature: ai.FeatureCommit}).Manifest.PromptSHA256; got != m.PromptSHA256 {
		t.Error("the same context gave another hash")
	}

	// --path: the selected commit only, and the index is untouched.
	sel := r.mustPrepare(t, ai.Request{Feature: ai.FeatureCommit, Paths: []string{"other.txt"}})
	if !strings.Contains(sel.Prompt(), "+other-content") || strings.Contains(sel.Prompt(), "notes.txt") {
		t.Errorf("selected paths:\n%s", sel.Prompt())
	}
	if after := git(t, r.wt, "status", "--porcelain"); after != before {
		t.Errorf("gathering changed the index:\n%s\n%s", before, after)
	}

	d := r.draft(t, sel)
	if d.State != ai.DraftReady || len(d.Candidates) != 1 {
		t.Fatalf("draft %+v", d)
	}
	c := d.Candidates[0]
	var in struct {
		Message string   `json:"message"`
		Paths   []string `json:"paths"`
	}
	if err := json.Unmarshal(c.Input, &in); err != nil {
		t.Fatal(err)
	}
	if c.Operation != "commit.prepare" || c.Tool != "agentflow_commit_prepare" || in.Message != "feat: add notes\n\nAdd notes.txt with a greeting." || !slices.Equal(in.Paths, []string{"other.txt"}) {
		t.Errorf("candidate %s %s %+v", c.Operation, c.Tool, in)
	}
	if want := []string{"agentflow", "commit", "--prepare", "-m", in.Message, "--path", "other.txt"}; !slices.Equal(c.CLI, want) {
		t.Errorf("cli %q", c.CLI)
	}
}

// A secret file's content stays out of the prompt however git writes its
// header: without prefixes, with a quoted name, or renamed to a name that
// is not secret.
func TestCommitDraftSecretFileHeaders(t *testing.T) {
	for name, setup := range map[string]func(t *testing.T, r *rig){
		"diff.noprefix": func(t *testing.T, r *rig) {
			git(t, r.wt, "config", "diff.noprefix", "true")
			write(t, r.wt, ".env", "V=never-sent-value\n")
			git(t, r.wt, "add", ".env")
		},
		"diff.mnemonicPrefix": func(t *testing.T, r *rig) {
			git(t, r.wt, "config", "diff.mnemonicPrefix", "true")
			write(t, r.wt, "dir/.env", "V=never-sent-value\n")
			git(t, r.wt, "add", "dir/.env")
		},
		"a quoted name": func(t *testing.T, r *rig) {
			write(t, r.wt, "clé.pem", "never-sent-value\n")
			git(t, r.wt, "add", "clé.pem")
		},
		"custom prefixes": func(t *testing.T, r *rig) {
			git(t, r.wt, "config", "diff.srcPrefix", "left/")
			git(t, r.wt, "config", "diff.dstPrefix", "right/")
			write(t, r.wt, ".env", "V=never-sent-value\n")
			git(t, r.wt, "add", ".env")
		},
		"renamed from a secret file": func(t *testing.T, r *rig) {
			lines := strings.Repeat("a line that stays the same\n", 20)
			write(t, r.wt, ".env", lines+"V=never-sent-value\n")
			git(t, r.wt, "add", ".env")
			git(t, r.wt, "commit", "-q", "-m", "add env")
			git(t, r.wt, "mv", ".env", "settings.txt")
			write(t, r.wt, "settings.txt", lines+"V=2\n")
			git(t, r.wt, "add", "settings.txt")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			setup(t, r)
			p := r.mustPrepare(t, ai.Request{Feature: ai.FeatureCommit})
			if strings.Contains(p.Prompt(), "never-sent-value") {
				t.Errorf("the prompt holds the secret file's content:\n%s", p.Prompt())
			}
		})
	}
}

func TestPRDraft(t *testing.T) {
	r := newRig(t)
	if _, err := r.prepare(t, ai.Request{Feature: ai.FeaturePR}); refusal(t, err) != "nothing_to_draft" {
		t.Fatalf("no commits on the branch: %v", err)
	}
	write(t, r.wt, "notes.txt", "hello\n")
	write(t, r.wt, ".env", "V=never-sent-value\n")
	write(t, r.wt, ".github/pull_request_template.md", "## Why this template\n")
	git(t, r.wt, "add", ".")
	git(t, r.wt, "commit", "-q", "-m", "feat: add notes", "-m", "Because.\n\nSigned-off-by: Some One <some.one@example.invalid>\nCc <other@example.invalid>")
	write(t, r.wt, "uncommitted.txt", "not-in-the-pr\n")
	git(t, r.wt, "add", "uncommitted.txt")

	p := r.mustPrepare(t, ai.Request{Feature: ai.FeaturePR})
	m := p.Manifest
	want := []ai.PartKind{ai.PartCommits, ai.PartDiffStat, ai.PartDiff, ai.PartRules, ai.PartTemplate}
	if !slices.Equal(kinds(m), want) {
		t.Errorf("parts %v, want %v", kinds(m), want)
	}
	prompt := p.Prompt()
	for _, s := range []string{"feat: add notes", "Because.", "+hello", "## Why this template", "64"} {
		if !strings.Contains(prompt, s) {
			t.Errorf("the prompt does not hold %q:\n%s", s, prompt)
		}
	}
	for _, s := range []string{"never-sent-value", "not-in-the-pr", "some.one@example.invalid", "other@example.invalid", "Some One", "dev@example.invalid"} {
		if strings.Contains(prompt, s) {
			t.Errorf("the prompt holds %q", s)
		}
	}
	r.sameManifest(t, p, ai.Request{Feature: ai.FeaturePR})
	d := r.draft(t, p)
	if d.State != ai.DraftReady || len(d.Candidates) != 1 || d.Candidates[0].Tool != "agentflow_pr_prepare" {
		t.Fatalf("draft %+v", d)
	}
	if got, want := d.Candidates[0].CLI, []string{"agentflow", "pr", "--prepare", "--title", "feat: add notes", "--body", "Adds notes.txt."}; !slices.Equal(got, want) {
		t.Errorf("cli %q", got)
	}
}

// A PR template that links out of the worktree, or into .git, is not read.
func TestPRDraftTemplateStaysInTheWorktree(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("never-sent-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"outside": outside, "secret file": "../.env"} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			write(t, r.wt, ".env", "never-sent-value\n")
			write(t, r.wt, "notes.txt", "hello\n")
			if err := os.MkdirAll(filepath.Join(r.wt, ".github"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(r.wt, ".github", "pull_request_template.md")); err != nil {
				t.Fatal(err)
			}
			git(t, r.wt, "add", "notes.txt")
			git(t, r.wt, "commit", "-q", "-m", "feat: add notes")
			p := r.mustPrepare(t, ai.Request{Feature: ai.FeaturePR})
			if strings.Contains(p.Prompt(), "never-sent-value") {
				t.Errorf("the prompt holds the linked file:\n%s", p.Prompt())
			}
		})
	}
}

func TestPRDraftNoBase(t *testing.T) {
	r := newRig(t)
	git(t, r.wt, "branch", "-m", "main", "trunk")
	if _, err := r.prepare(t, ai.Request{Feature: ai.FeaturePR}); refusal(t, err) != "no_base" {
		t.Fatalf("no base: %v", err)
	}
}

// ticket creates a task with description in a filesystem task store and
// binds the branch to it.
func (r *rig) ticket(t *testing.T, description string) string {
	t.Helper()
	write(t, r.wt, ".agentflow/config.yml", "commit: {require_key: false}\ntask_management: {provider: filesystem}\n")
	a := r.app(t)
	a.Gate.Autonomy.Local, a.Gate.Autonomy.Remote = config.Auto, config.Auto
	v, err := a.CreateTicket(context.Background(), "", "", "Greet people", description, "", "")
	if pending, ok := v.(*app.Pending); ok && err == nil {
		v, err = a.CreateTicket(context.Background(), "", "", "Greet people", description, "", pending.ConfirmToken)
	}
	if err != nil {
		t.Fatal(err)
	}
	created, ok := v.(map[string]string)
	if !ok {
		t.Fatalf("created %#v", v)
	}
	git(t, r.wt, "config", "--local", "branch.task.agentflowTaskRef", created["key"])
	return created["key"]
}

func TestACDraft(t *testing.T) {
	r := newRig(t)
	if _, err := r.prepare(t, ai.Request{Feature: ai.FeatureAC}); refusal(t, err) != "no_task" {
		t.Fatalf("no task: %v", err)
	}
	key := r.ticket(t, "Acceptance criteria\n[ ] Notes hold a greeting\n[x] The repository exists\n")
	if _, err := r.prepare(t, ai.Request{Feature: ai.FeatureAC}); refusal(t, err) != "nothing_to_draft" {
		t.Fatalf("no changes: %v", err)
	}
	write(t, r.wt, "notes.txt", "hello\n")
	git(t, r.wt, "add", "notes.txt")
	git(t, r.wt, "commit", "-q", "-m", "feat: add notes")
	write(t, r.wt, "staged.txt", "staged-content\n")
	git(t, r.wt, "add", "staged.txt")

	state := filepath.Join(strings.TrimSpace(git(t, r.wt, "rev-parse", "--path-format=absolute", "--git-common-dir")), "agentflow")
	before, _ := filepath.Glob(filepath.Join(state, "*"))

	p := r.mustPrepare(t, ai.Request{Feature: ai.FeatureAC})
	m := p.Manifest
	want := []ai.PartKind{ai.PartCriteria, ai.PartDiff, ai.PartDiff, ai.PartCommits}
	if !slices.Equal(kinds(m), want) {
		t.Errorf("parts %v, want %v", kinds(m), want)
	}
	if len(m.Targets) != 2 || m.Targets[0].ID != "Notes hold a greeting" || m.Targets[0].Done || !m.Targets[1].Done {
		t.Errorf("targets %+v", m.Targets)
	}
	for _, s := range []string{"+hello", "+staged-content", "Notes hold a greeting", "feat: add notes"} {
		if !strings.Contains(p.Prompt(), s) {
			t.Errorf("the prompt does not hold %q:\n%s", s, p.Prompt())
		}
	}
	if after, _ := filepath.Glob(filepath.Join(state, "*")); !slices.Equal(before, after) {
		t.Errorf("gathering wrote the checklist: %v, %v", before, after)
	}

	r.sameManifest(t, p, ai.Request{Feature: ai.FeatureAC})
	d := r.draft(t, p)
	if d.State != ai.DraftReady || len(d.Candidates) != 1 {
		t.Fatalf("draft %+v", d)
	}
	c := d.Candidates[0]
	var in map[string]any
	if err := json.Unmarshal(c.Input, &in); err != nil {
		t.Fatal(err)
	}
	if c.Tool != "agentflow_ac_update" || in["key"] != key || in["item"] != "Notes hold a greeting" || in["done"] != true || in["evidence"] != "notes.txt is added" || len(in) != 4 {
		t.Errorf("candidate %s %v", c.Tool, in)
	}
	if want := []string{"agentflow", "ac", "done", "Notes hold a greeting", "--key", key, "--evidence", "notes.txt is added"}; !slices.Equal(c.CLI, want) {
		t.Errorf("cli %q", c.CLI)
	}
	// An explicit key reads the same task.
	if p := r.mustPrepare(t, ai.Request{Feature: ai.FeatureAC, Key: key}); len(p.Manifest.Targets) != 2 {
		t.Errorf("targets with a key: %+v", p.Manifest.Targets)
	}
}

func TestACDraftNoCriteria(t *testing.T) {
	r := newRig(t)
	r.ticket(t, "Just words.")
	if _, err := r.prepare(t, ai.Request{Feature: ai.FeatureAC}); refusal(t, err) != "no_criteria" {
		t.Fatalf("no criteria: %v", err)
	}
}

// With diff.noprefix=true a changed .env still has its content left out of
// every feature's prompt, and the manifest says so.
func TestDraftsIgnoreNoPrefixConfig(t *testing.T) {
	r := newRig(t)
	r.ticket(t, "Acceptance criteria\n[ ] Notes hold a greeting\n")
	git(t, r.wt, "config", "diff.noprefix", "true")
	write(t, r.wt, ".env", "V=never-sent-one\n")
	write(t, r.wt, "notes.txt", "hello\n")
	git(t, r.wt, "add", "-f", ".env", "notes.txt")

	check := func(feature ai.FeatureID) {
		t.Helper()
		p := r.mustPrepare(t, ai.Request{Feature: feature})
		if strings.Contains(p.Prompt(), "never-sent") {
			t.Errorf("%s: the prompt holds the .env content:\n%s", feature, p.Prompt())
		}
		if !strings.Contains(p.Prompt(), "+hello") {
			t.Errorf("%s: the other file's change is missing:\n%s", feature, p.Prompt())
		}
		found := false
		for _, pt := range p.Manifest.Parts {
			found = found || slices.Contains(pt.Excluded, ai.Exclusion{Path: ".env", Reason: ai.ExcludedSecretFile})
		}
		if !found {
			t.Errorf("%s: .env is not listed as left out: %+v", feature, p.Manifest.Parts)
		}
	}
	check(ai.FeatureCommit)
	check(ai.FeatureAC) // the staged diff
	git(t, r.wt, "commit", "-q", "-m", "feat: add notes")
	check(ai.FeaturePR)
	check(ai.FeatureAC) // the branch's diff
	write(t, r.wt, ".env", "V=never-sent-two\n")
	git(t, r.wt, "add", "-f", ".env")
	check(ai.FeatureAC) // both
}
