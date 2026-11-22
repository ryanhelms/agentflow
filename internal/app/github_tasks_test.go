package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nscaledev/devflow/internal/config"
	"github.com/nscaledev/devflow/internal/flowmap"
	"github.com/nscaledev/devflow/internal/gitx"
	"github.com/nscaledev/devflow/internal/policy"
	"github.com/nscaledev/devflow/internal/provider"
)

type fakeIssues struct {
	task      provider.Task
	query     provider.TaskQuery
	writes    []string
	draft     provider.TaskDraft
	assignErr error
}

func (f *fakeIssues) Get(_ context.Context, r provider.Ref) (provider.Task, error) {
	if r != f.task.Ref {
		return provider.Task{}, provider.ErrNotFound
	}
	return f.task, nil
}
func (f *fakeIssues) Find(_ context.Context, q provider.TaskQuery) ([]provider.Task, error) {
	f.query = q
	return []provider.Task{f.task}, nil
}
func (f *fakeIssues) Create(_ context.Context, d provider.TaskDraft, o provider.WriteOptions) (provider.WriteResult, error) {
	f.draft = d
	f.writes = append(f.writes, o.OperationID)
	return provider.WriteResult{Ref: provider.Ref{Instance: issuesInstance, Scope: d.Scope, ID: "100"}, Status: provider.Applied}, nil
}
func (f *fakeIssues) Transition(_ context.Context, r provider.Ref, target provider.TaskState, o provider.WriteOptions) (provider.WriteResult, error) {
	if r != f.task.Ref || target.ID != "closed" {
		return provider.WriteResult{}, errors.New("unexpected transition")
	}
	if o.CheckTask != nil {
		if err := o.CheckTask(f.task); err != nil {
			return provider.WriteResult{}, err
		}
	}
	f.writes = append(f.writes, o.OperationID)
	f.task.State = target
	return provider.WriteResult{Ref: r, Status: provider.Applied}, nil
}
func (f *fakeIssues) AssignToMe(_ context.Context, r provider.Ref, o provider.WriteOptions) (provider.WriteResult, error) {
	if r != f.task.Ref {
		return provider.WriteResult{}, errors.New("unexpected assignment")
	}
	f.writes = append(f.writes, o.OperationID)
	return provider.WriteResult{Ref: r, Status: provider.Applied}, f.assignErr
}
func (f *fakeIssues) Comment(_ context.Context, r provider.Ref, _ string, o provider.WriteOptions) (provider.WriteResult, error) {
	if r != f.task.Ref {
		return provider.WriteResult{}, errors.New("unexpected comment target")
	}
	if o.CheckTask != nil {
		if err := o.CheckTask(f.task); err != nil {
			return provider.WriteResult{}, err
		}
	}
	f.writes = append(f.writes, o.OperationID)
	return provider.WriteResult{Ref: r, Status: provider.Applied}, nil
}

func issueApp(t *testing.T) (*App, *fakeIssues) {
	t.Helper()
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	dir := newRepo(t)
	r := &gitx.Repo{Dir: dir}
	mustGit(t, r, "switch", "main")
	full := filepath.Join(dir, ".devflow", "config.yml")
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("task_management:\n  provider: github-issues\n  settings:\n    labels: [triage]\n    search: {limit: 4}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// These fixtures exercise explicit cross-repository inputs and confirmation.
	// A personal default supplies shorthand without setting a repository ceiling.
	writeUserDefinition(t, "github-issues: {task_management: {settings: {repository: Org/Repo}}}")
	mustGit(t, r, "add", ".devflow/config.yml")
	mustGit(t, r, "commit", "-qm", "chore: use github issues")
	mustGit(t, r, "switch", "-c", "feature")
	a, err := Open(t.Context(), dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIssues{task: provider.Task{Ref: provider.Ref{Instance: issuesInstance, Scope: "org/repo", ID: "42"}, Title: "Add quota", Description: "- [ ] Write tests", State: provider.TaskState{ID: "open", Name: "Open"}, Assignee: "me"}}
	a.tasks = f
	a.Gate.Autonomy.Remote = config.Confirm
	return a, f
}

func mustGit(t *testing.T, r *gitx.Repo, args ...string) {
	t.Helper()
	if _, err := r.Git(t.Context(), args...); err != nil {
		t.Fatal(err)
	}
}

func confirmation(t *testing.T, v any) string {
	t.Helper()
	p, ok := v.(*Pending)
	if !ok || p.Status != "needs_confirmation" || p.ConfirmToken == "" {
		t.Fatalf("expected confirmation, got %#v", v)
	}
	return p.ConfirmToken
}

func TestIssueWorkAndContext(t *testing.T) {
	a, f := issueApp(t)
	ctx := t.Context()
	work, err := a.MyWork(ctx)
	if err != nil || len(work) != 1 || work[0].Key != "org/repo#42" || f.query != (provider.TaskQuery{Scope: "org/repo", AssignedToMe: true, OpenOnly: true, Limit: 4}) {
		t.Fatalf("work=%+v query=%+v err=%v", work, f.query, err)
	}
	tc, err := a.Context(ctx, "#42")
	if err != nil || tc.Key != work[0].Key || tc.Assignee != "me" || tc.Status != "Open" || tc.Criteria == nil || tc.FlowNext[flowmap.PRMerged] != "closed" || !strings.Contains(tc.Text, untrustedNote) || len(tc.Warnings) > 0 {
		t.Fatalf("context=%+v err=%v", tc, err)
	}
	if _, err := os.Stat(filepath.Join(a.Checklists(ctx).Dir, f.task.Ref.StorageKey()+".json")); err != nil {
		t.Fatal(err)
	}
	updated, err := a.UpdateChecklist(ctx, "#42", "Write tests", true, "abc", false, "")
	if err != nil || updated == nil {
		t.Fatal(err)
	}
	cl, err := a.Checklist(ctx, tc.Key)
	if err != nil || cl.Key != tc.Key || !cl.Items[0].Done || cl.Items[0].Evidence != "abc" {
		t.Fatalf("checklist=%+v err=%v", cl, err)
	}
	a.issueSettings().Repository = nil
	if _, err := a.Context(ctx, "#42"); err == nil {
		t.Fatal("accepted shorthand without repository")
	}
	for _, bad := range []string{"42", "(#42)", "org/repo#01", "../repo#42", "org/repo#-1", "jira/org%2Frepo/42"} {
		if _, err := a.Context(ctx, bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	mustGit(t, a.Repo, "remote", "add", "origin", "git@github.com:Fork/Repo.git")
	mustGit(t, a.Repo, "remote", "add", "upstream", "https://github.com/Org/Repo.git")
	if ref, err := a.issueRef(ctx, "#42"); err != nil || ref != f.task.Ref {
		t.Fatalf("upstream ref=%v err=%v", ref, err)
	}
	mustGit(t, a.Repo, "remote", "set-url", "upstream", "https://notgithub.com/org/repo")
	if _, err := a.issueRef(ctx, "#42"); err == nil {
		t.Fatal("accepted ambiguous default")
	}
	if ref, err := a.issueRef(ctx, f.task.Ref.String()); err != nil || ref != f.task.Ref {
		t.Fatalf("explicit ref=%v err=%v", ref, err)
	}
}

func TestIssueWriteConfirmationAndRetry(t *testing.T) {
	a, f := issueApp(t)
	ctx := t.Context()
	t.Setenv("DEVFLOW_OPERATION_ID", "event-1")
	v, err := a.Comment(ctx, "#42", "progress", "")
	if err != nil {
		t.Fatal(err)
	}
	tok := confirmation(t, v)
	f.task.Ref.Scope = "fork/repo"
	if _, err := a.Comment(ctx, "fork/repo#42", "progress", tok); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("target change: %v", err)
	}
	f.task.Ref.Scope = "org/repo"
	if len(f.writes) != 0 {
		t.Fatal("preview wrote a comment")
	}
	v, err = a.Comment(ctx, "ORG/repo#42", "progress", tok)
	if err != nil || v.(map[string]any)["commented"] != true {
		t.Fatalf("comment=%v err=%v", v, err)
	}
	a.Gate.Autonomy.Remote = config.Auto
	create := func(token string) (any, error) {
		return a.CreateTicket(ctx, "", "Task", "Follow up", "Details", "#42", token)
	}
	v, err = create("")
	if err != nil {
		t.Fatal(err)
	}
	tok = confirmation(t, v)
	other := "other/repo"
	a.issueSettings().Repository = &other
	if _, err = create(tok); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("create token accepted change: %v", err)
	}
	repo := "org/repo"
	a.issueSettings().Repository = &repo
	v, err = create(tok)
	if err != nil || v.(map[string]string)["key"] != "org/repo#100" {
		t.Fatalf("create=%v err=%v", v, err)
	}
	if f.draft.Scope != repo || !slices.Equal(f.draft.Labels, []string{"triage"}) || !strings.Contains(f.draft.Description, "Related issue: org/repo#42") || f.writes[1] == "" {
		t.Fatalf("draft=%+v writes=%v", f.draft, f.writes)
	}
	// A new process and approval for the same event yield the same adapter ID.
	a2, err := Open(ctx, a.Repo.Dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	a2.tasks = f
	a = a2
	v, err = create("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = create(confirmation(t, v)); err != nil {
		t.Fatal(err)
	}
	if f.writes[1] != f.writes[2] {
		t.Fatalf("unstable operation identity: %v", f.writes)
	}
	t.Setenv("DEVFLOW_OPERATION_ID", "event-2")
	v, err = create("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = create(confirmation(t, v)); err != nil {
		t.Fatal(err)
	}
	if f.writes[2] == f.writes[3] {
		t.Fatal("distinct event collapsed into previous create")
	}
}

func TestIssueTransitions(t *testing.T) {
	a, f := issueApp(t)
	ctx := t.Context()
	v, err := a.Transition(ctx, "#42", flowmap.PRMerged, true, "")
	if err != nil || v.(TransitionResult) != (TransitionResult{Key: "org/repo#42", Event: flowmap.PRMerged, From: "open", To: "closed"}) {
		t.Fatalf("plan=%v err=%v", v, err)
	}
	a.issueSettings().Transitions["#qa"] = "closed"
	v, err = a.Transition(ctx, "#42", "#qa", false, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := confirmation(t, v)
	if _, err := a.Transition(ctx, "#42", flowmap.PRMerged, false, tok); !errors.Is(err, policy.ErrToken) {
		t.Fatalf("event binding: %v", err)
	}
	if len(f.writes) != 0 {
		t.Fatal("preview mutated issue")
	}
	v, err = a.Transition(ctx, "#42", "#qa", false, tok)
	if err != nil || !v.(TransitionResult).Applied {
		t.Fatalf("transition=%v err=%v", v, err)
	}
	a.issueSettings().Transitions["#start"] = "open"
	for _, ev := range []string{"#start", "#qa", "unknown"} {
		v, err = a.Transition(ctx, "#42", ev, false, "")
		if err != nil || v.(TransitionResult).To != "" || len(f.writes) != 1 {
			t.Fatalf("%s => %v err=%v", ev, v, err)
		}
	}
}

func TestIssueStartBindingAndPortableTrailers(t *testing.T) {
	a, f := issueApp(t)
	ctx := t.Context()
	t.Setenv("HOME", t.TempDir())
	v, err := a.Start(ctx, "#42", true, "")
	if err != nil {
		t.Fatal(err)
	}
	tok := confirmation(t, v)
	if len(f.writes) > 0 || a.CurrentKey(ctx) != "" {
		t.Fatal("start preview had side effects")
	}
	f.assignErr = errors.New("temporary assignment failure")
	v, err = a.Start(ctx, "#42", true, tok)
	if !errors.Is(err, f.assignErr) {
		t.Fatalf("start=%v err=%v", v, err)
	}
	res := v.(StartResult)
	if res.Worktree == "" || a.CurrentKey(ctx) != "" || res.Assigned {
		t.Fatalf("start=%+v", res)
	}
	checkout, err := Open(ctx, res.Worktree, "test")
	if err != nil {
		t.Fatal(err)
	}
	checkout.tasks = f
	if k := checkout.CurrentKey(ctx); k != "org/repo#42" {
		t.Fatalf("wrong binding %q", k)
	}
	f.assignErr = nil
	checkout.Gate.Autonomy.Remote = config.Auto
	checkout.Gate.Autonomy.Local = config.Auto
	v, err = checkout.Start(ctx, "#42", false, "")
	if err != nil || !v.(StartResult).Assigned {
		t.Fatalf("retry=%v err=%v", v, err)
	}
	p := filepath.Join(res.Worktree, "fix.txt")
	if err := os.WriteFile(p, []byte("fix"), 0600); err != nil {
		t.Fatal(err)
	}
	cc, err := checkout.CommitContext(ctx, []string{"fix.txt"})
	trailer := provider.TaskTrailer + f.task.Ref.String()
	if err != nil || cc.Key != res.Key || !strings.Contains(cc.Rules, "\n"+trailer) || !strings.Contains(cc.Ticket, f.task.Title) {
		t.Fatalf("commit context=%+v err=%v", cc, err)
	}
	mustGit(t, checkout.Repo, "commit", "-qm", "fix: adjust limit (#99)", "-m", trailer)
	mustGit(t, checkout.Repo, "remote", "add", "origin", "https://github.com/org/repo.git")
	checkout.Gate.Autonomy.Remote = config.Confirm
	for _, body := range []string{"", "Custom body"} {
		v, err = checkout.PROpen(ctx, PROpenInput{Title: "fix: adjust limit", Body: body}, "")
		if err != nil {
			t.Fatal(err)
		}
		confirmation(t, v)
		prBody := v.(*Pending).Summary.(map[string]any)["body"].(string)
		if !strings.HasSuffix(prBody, trailer+"\n") || strings.Contains(prBody, "Jira:") {
			t.Fatalf("body=%q", prBody)
		}
	}
	if _, err := checkout.PROpen(ctx, PROpenInput{Title: "fix: adjust limit", Body: "Task-Ref: github-issues/other%2Frepo/42"}, ""); err == nil {
		t.Fatal("accepted conflicting PR body")
	}
	// Clone only from the local repository, with no shared branch config.
	clone := filepath.Join(t.TempDir(), "fresh")
	mustGit(t, checkout.Repo, "clone", "--local", "--branch", res.Branch, checkout.Repo.Dir, clone)
	fresh, err := Open(ctx, clone, "test")
	if err != nil {
		t.Fatal(err)
	}
	fresh.tasks = f
	mustGit(t, fresh.Repo, "config", "user.email", "dev@example.com")
	mustGit(t, fresh.Repo, "config", "user.name", "Dev")
	mustGit(t, fresh.Repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	if got := fresh.CurrentKey(ctx); got != res.Key {
		t.Fatalf("fresh clone key=%q", got)
	}
	mustGit(t, fresh.Repo, "commit", "-q", "--allow-empty", "-m", "fix: tidy up (#123)")
	keys, err := fresh.Keys(ctx, "HEAD^..HEAD")
	if err != nil || len(keys) != 0 {
		t.Fatalf("squash suffix selected %v err=%v", keys, err)
	}
	effects, _, err := fresh.Preview(ctx, "fix(PLAT-1): tidy up (#42)\n\nSee #123")
	if err != nil || len(effects) != 0 {
		t.Fatalf("implicit preview: %v err=%v", effects, err)
	}
	effects, _, err = fresh.Preview(ctx, "fix: tidy up (#99)\n\n"+trailer)
	if err != nil || len(effects) != 1 || effects[0].Key != res.Key {
		t.Fatalf("explicit preview: %v err=%v", effects, err)
	}
}

func TestIssueBranchAmbiguityAndLocalPolicy(t *testing.T) {
	a, f := issueApp(t)
	ctx := t.Context()
	a.Gate.Autonomy.Local = config.Suggest
	a.Gate.Autonomy.Remote = config.Auto
	v, err := a.Start(ctx, "#42", false, "")
	if err != nil || v.(*Pending).Status != "suggest" || a.Repo.Branch(ctx) != "feature" || len(f.writes) != 0 {
		t.Fatalf("start under local suggest=%v writes=%v err=%v", v, f.writes, err)
	}
	a.Gate.Autonomy.Local = config.Auto
	// An existing branch with the generated name has no authority to bind an issue.
	branch := v.(*Pending).Summary.(map[string]any)["branch"].(string)
	mustGit(t, a.Repo, "branch", branch)
	if _, err := a.Start(ctx, "#42", false, ""); err == nil || len(f.writes) != 0 {
		t.Fatalf("unbound collision: writes=%v err=%v", f.writes, err)
	}
	mustGit(t, a.Repo, "commit", "-q", "--allow-empty", "-m", "fix: adjust limit", "-m", provider.TaskTrailer+f.task.Ref.String()+"\nTask-Ref: github-issues/fork%2Frepo/42")
	if _, err := a.Comment(ctx, "", "progress", ""); err == nil || len(f.writes) != 0 {
		t.Fatalf("ambiguous branch: writes=%v err=%v", f.writes, err)
	}
	if _, err := a.Comment(ctx, "org/repo#42", "progress", ""); err != nil {
		t.Fatal(err)
	}
	mustGit(t, a.Repo, "config", "--local", "branch.feature.devflowTaskRef", "../../42")
	if _, err := a.Context(ctx, ""); err == nil {
		t.Fatal("invalid local binding accepted")
	}
}
