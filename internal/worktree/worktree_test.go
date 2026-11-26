package worktree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nscaledev/agentflow/internal/config"
	"github.com/nscaledev/agentflow/internal/gitx"
)

func userWith(checkouts map[string]string) *config.User {
	u := &config.User{}
	u.Worktree.Checkouts = checkouts
	return u
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v %s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func isolate(t *testing.T) string {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for k, v := range map[string]string{"GIT_AUTHOR_NAME": "Dev", "GIT_AUTHOR_EMAIL": "dev@example.com", "GIT_COMMITTER_NAME": "Dev", "GIT_COMMITTER_EMAIL": "dev@example.com"} {
		t.Setenv(k, v)
	}
	return tmp
}

// fixture makes a bare origin whose default branch is develop and a primary clone at ~/GitHub/app.
func fixture(t *testing.T) (home, primary string) {
	home = isolate(t)
	bare := filepath.Join(home, "origin", "app.git")
	git(t, home, "init", "-q", "--bare", "-b", "develop", bare)
	primary = filepath.Join(home, "GitHub", "app")
	git(t, home, "clone", "-q", bare, primary)
	write(t, filepath.Join(primary, "go.mod"), "module example.com/app\n\ngo 1.22\n")
	git(t, primary, "add", ".")
	git(t, primary, "commit", "-q", "-m", "chore: init")
	git(t, primary, "push", "-q", "origin", "HEAD:develop")
	git(t, primary, "remote", "set-head", "origin", "develop")
	return home, primary
}

func addWorktree(t *testing.T, s Settings, primary, branch string) string {
	t.Helper()
	dir := s.Path("app", branch)
	git(t, primary, "worktree", "add", "-q", "-b", branch, dir, "origin/develop")
	return dir
}

func commitFile(t *testing.T, dir, name string) {
	t.Helper()
	write(t, filepath.Join(dir, name), name+"\n")
	git(t, dir, "add", name)
	git(t, dir, "commit", "-q", "-m", "feat: add "+name)
}

// squashMerge lands branch on origin/develop as a single new commit, like a GitHub squash merge.
func squashMerge(t *testing.T, primary, branch string) {
	t.Helper()
	git(t, primary, "fetch", "-q", "origin")
	git(t, primary, "merge", "-q", "--squash", "origin/"+branch)
	git(t, primary, "commit", "-q", "-m", "feat: squash "+branch)
	git(t, primary, "push", "-q", "origin", "HEAD:develop")
}

func fakeGH(prs map[string]string) GH {
	return func(_ context.Context, _ string, args ...string) (string, error) {
		if len(args) > 2 && args[0] == "pr" && args[1] == "view" {
			if out, ok := prs[args[2]]; ok {
				return out, nil
			}
		}
		return "", errors.New("no pull requests found")
	}
}

func TestResolveDefaultsAndOverrides(t *testing.T) {
	home := isolate(t)
	s := Resolve(&config.Repo{}, &config.User{})
	if s.Root != filepath.Join(home, ".worktrees") || s.Checkout("agent-acp") != filepath.Join(home, "GitHub", "agent-acp") {
		t.Fatalf("defaults: %+v %s", s, s.Checkout("agent-acp"))
	}
	if got := s.Path("app", "feat/x"); got != filepath.Join(home, ".worktrees", "app", "feat", "x") {
		t.Fatalf("path %s", got)
	}
	u := &config.User{}
	u.Worktree.Root = "~/personal"
	u.Worktree.Checkouts = map[string]string{"agent-acp": "~/src/acp"}
	r := &config.Repo{}
	if s := Resolve(r, u); s.Root != filepath.Join(home, "personal") || s.Checkout("agent-acp") != filepath.Join(home, "src", "acp") {
		t.Fatalf("personal: %+v", s)
	}
	r.Worktree.Root = "~/wt"
	if s := Resolve(r, u); s.Root != filepath.Join(home, "wt") {
		t.Fatalf("repository root should win: %s", s.Root)
	}
}

func TestRecordIsExcludedAndListed(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	dir := addWorktree(t, s, primary, "issue-1-quota")
	ctx := t.Context()
	rec := Record{Issue: "o/app#1", Branch: "issue-1-quota", Base: "origin/develop", Repo: "app", Created: time.Unix(1700000000, 0).UTC()}
	if err := WriteRecord(ctx, dir, rec); err != nil {
		t.Fatal(err)
	}
	if err := WriteRecord(ctx, dir, rec); err != nil { // idempotent exclude
		t.Fatal(err)
	}
	if out := git(t, dir, "status", "--porcelain"); out != "" {
		t.Fatalf("record is not excluded: %q", out)
	}
	exclude, _ := os.ReadFile(filepath.Join(primary, ".git", "info", "exclude"))
	if strings.Count(string(exclude), "/"+RecordFile) != 1 {
		t.Fatalf("exclude = %q", exclude)
	}
	got, err := ReadRecord(dir)
	if err != nil || got != rec {
		t.Fatalf("record %+v %v", got, err)
	}
	entries, err := List(ctx, dir)
	if err != nil || len(entries) != 2 || !entries[0].Primary || entries[0].Path != primary || entries[1].Branch != "issue-1-quota" {
		t.Fatalf("entries %+v %v", entries, err)
	}
	if !IsLinked(ctx, dir) || IsLinked(ctx, primary) {
		t.Fatal("linked detection")
	}
	if RepoName(ctx, &gitx.Repo{Dir: dir}) != "app" {
		t.Fatalf("repo name %q", RepoName(ctx, &gitx.Repo{Dir: dir}))
	}
}

func TestWriteGoWork(t *testing.T) {
	home, primary := fixture(t)
	write(t, filepath.Join(primary, "multiplayer", "go.mod"), "module example.com/app/multiplayer\n\ngo 1.24.2\n")
	git(t, primary, "add", ".")
	git(t, primary, "commit", "-q", "-m", "chore: add module")
	git(t, primary, "push", "-q", "origin", "HEAD:develop")
	s := Resolve(&config.Repo{Worktree: config.Worktree{GoWork: &config.GoWork{
		Use: []string{".", "multiplayer"},
		Siblings: map[string]config.GoWorkSibling{
			"agent-acp.local/sdks/go":   {Repo: "agent-acp", Path: "sdks/go"},
			"agent-acp.local/transport": {Repo: "agent-acp-transport", Path: "."},
			"agent-acp.local/other":     {Repo: "other", Path: "lib"},
		},
	}}}, userWith(map[string]string{"other": "~/src/other"}))
	dir := addWorktree(t, s, primary, "PLAT-1-quota")
	// agent-acp has a worktree for the same branch; agent-acp-transport one for the same issue.
	sameBranch := s.Path("agent-acp", "PLAT-1-quota")
	write(t, filepath.Join(sameBranch, ".git"), "gitdir: elsewhere\n")
	sameIssue := s.Path("agent-acp-transport", "feat/PLAT-1-transport")
	write(t, filepath.Join(sameIssue, ".git"), "gitdir: elsewhere\n")
	write(t, filepath.Join(sameIssue, RecordFile), "issue: PLAT-1\nbranch: feat/PLAT-1-transport\n")
	res, err := WriteGoWork(t.Context(), dir, s, Record{Issue: "PLAT-1", Branch: "PLAT-1-quota"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "go.work"))
	want := "go 1.24.2\n\nuse (\n\t.\n\t./multiplayer\n)\n\n" +
		"replace agent-acp.local/other => " + filepath.Join(home, "src", "other", "lib") + "\n" +
		"replace agent-acp.local/sdks/go => " + filepath.Join(sameBranch, "sdks", "go") + "\n" +
		"replace agent-acp.local/transport => " + sameIssue + "\n"
	if !strings.HasSuffix(string(b), want) || res.Go != "1.24.2" {
		t.Fatalf("go.work =\n%s\nwant suffix\n%s", b, want)
	}
	write(t, filepath.Join(dir, "go.work.sum"), "")
	if out := git(t, dir, "status", "--porcelain"); out != "" {
		t.Fatalf("go.work is not excluded: %q", out)
	}
	if res, err := WriteGoWork(t.Context(), dir, Settings{}, Record{}); res != nil || err != nil {
		t.Fatalf("unconfigured go.work: %v %v", res, err)
	}
}

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.22", "1.22.0", 0}, {"1.24.2", "1.24", 1}, {"1.21", "1.9", 1}, {"1.23rc1", "1.22.5", 1}, {"", "1.20", -1}} {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compare(%q, %q) = %d", c.a, c.b, got)
		}
	}
}

func TestFinishSquashMergedWorktree(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	dir := addWorktree(t, s, primary, "issue-2-api")
	commitFile(t, dir, "api.go")
	git(t, dir, "push", "-q", "-u", "origin", "issue-2-api")
	squashMerge(t, primary, "issue-2-api")
	head := git(t, dir, "rev-parse", "HEAD")
	opt := Options{Protected: config.DefaultProtected, GH: fakeGH(map[string]string{
		"issue-2-api": `{"state":"MERGED","mergedAt":"2026-11-20T10:00:00Z","baseRefName":"develop","headRefOid":"` + head + `"}`,
	})}
	ctx := t.Context()
	plan, err := PlanFinish(ctx, dir, "", opt)
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != StateWouldFinish || plan.Primary != primary || len(plan.Actions) != 4 || !strings.Contains(plan.Actions[3], "push origin --delete issue-2-api") {
		t.Fatalf("plan %+v", plan.Outcome)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("planning removed the worktree")
	}
	out, err := Apply(ctx, plan)
	if err != nil || out.State != StateFinished {
		t.Fatalf("apply %+v %v", out, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("worktree still exists")
	}
	if refs := git(t, primary, "for-each-ref", "--format=%(refname)", "refs/heads/issue-2-api"); refs != "" {
		t.Fatalf("local branch remains: %s", refs)
	}
	if remote := git(t, primary, "ls-remote", "--heads", "origin", "issue-2-api"); remote != "" {
		t.Fatalf("remote branch remains: %s", remote)
	}
	again, err := PlanFinish(ctx, primary, "issue-2-api", opt)
	if err != nil || again.State != StateAlreadyFinished {
		t.Fatalf("second finish %+v %v", again, err)
	}
}

func TestFinishRefusals(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	ctx := t.Context()
	dirty := addWorktree(t, s, primary, "dirty")
	write(t, filepath.Join(dirty, "wip.go"), "package wip\n")
	open := addWorktree(t, s, primary, "open")
	commitFile(t, open, "open.go")
	git(t, open, "push", "-q", "origin", "open")
	extra := addWorktree(t, s, primary, "extra")
	commitFile(t, extra, "extra.go")
	git(t, extra, "push", "-q", "origin", "extra")
	merged := git(t, extra, "rev-parse", "HEAD")
	squashMerge(t, primary, "extra")
	commitFile(t, extra, "later.go") // work after the merge, never pushed
	opt := Options{Protected: config.DefaultProtected, GH: fakeGH(map[string]string{
		"dirty": `{"state":"MERGED","mergedAt":"x","baseRefName":"develop"}`,
		"open":  `{"state":"OPEN","mergedAt":"","baseRefName":"develop"}`,
		"extra": `{"state":"MERGED","mergedAt":"x","baseRefName":"develop","headRefOid":"` + merged + `"}`,
	})}
	for branch, state := range map[string]string{"dirty": StateDirty, "open": StateUnmerged, "extra": StateUnpushed, "develop": StateProtected} {
		_, err := PlanFinish(ctx, primary, branch, opt)
		var refused *RefusedError
		if !errors.As(err, &refused) || refused.Outcome.State != state {
			t.Errorf("%s: %v, want %s", branch, err, state)
		}
	}
	if _, err := PlanFinish(ctx, primary, "", opt); err == nil || !strings.Contains(err.Error(), "primary checkout") {
		t.Fatalf("finish in the primary checkout: %v", err)
	}
}

func TestFinishRefusesCommitsAfterMergedPR(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	dir := addWorktree(t, s, primary, "issue-3")
	commitFile(t, dir, "a.go")
	git(t, dir, "push", "-q", "-u", "origin", "issue-3")
	head := git(t, dir, "rev-parse", "HEAD")
	squashMerge(t, primary, "issue-3")
	commitFile(t, dir, "b.go") // follow-up work, pushed but in no merged PR
	git(t, dir, "push", "-q", "origin", "issue-3")
	opt := Options{Protected: config.DefaultProtected, GH: fakeGH(map[string]string{
		"issue-3": `{"state":"MERGED","mergedAt":"x","baseRefName":"develop","headRefOid":"` + head + `"}`,
	})}
	_, err := PlanFinish(t.Context(), dir, "", opt)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Outcome.State != StateUnmerged || !strings.Contains(refused.Outcome.Detail, "after the merged PR") {
		t.Fatalf("finish with commits after the merged PR: %v", err)
	}
	res, err := PlanSweep(t.Context(), []string{primary}, opt)
	if err != nil || len(res.Plans()) != 0 {
		t.Fatalf("sweep planned %d finishes: %v", len(res.Plans()), err)
	}
}

func TestOfflineMergeDetectionIgnoresNewBranches(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	ctx := t.Context()
	fresh := addWorktree(t, s, primary, "fresh")
	_ = fresh
	merged := addWorktree(t, s, primary, "merged")
	commitFile(t, merged, "m.go")
	git(t, merged, "push", "-q", "origin", "merged:develop")
	git(t, primary, "fetch", "-q", "origin")
	opt := Options{Offline: true, Protected: config.DefaultProtected}
	res, err := PlanSweep(ctx, []string{primary}, opt)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, w := range res.Worktrees {
		states[w.Branch] = w.State
	}
	if states["fresh"] != StateActive || states["merged"] != StateWouldFinish {
		t.Fatalf("states %v", states)
	}
}

func TestSweepReportsAndFinishes(t *testing.T) {
	_, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	ctx := t.Context()
	done := addWorktree(t, s, primary, "done")
	commitFile(t, done, "done.go")
	git(t, done, "push", "-q", "origin", "done")
	squashMerge(t, primary, "done")
	dirty := addWorktree(t, s, primary, "dirty")
	write(t, filepath.Join(dirty, "wip.go"), "x\n")
	unpushed := addWorktree(t, s, primary, "unpushed")
	commitFile(t, unpushed, "u.go")
	stale := addWorktree(t, s, primary, "stale")
	t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:00Z")
	commitFile(t, stale, "s.go")
	t.Setenv("GIT_COMMITTER_DATE", "")
	git(t, stale, "push", "-q", "origin", "stale")
	gone := addWorktree(t, s, primary, "gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	// An agent's own worktree under .claude/worktrees is swept too.
	claude := filepath.Join(primary, ".claude", "worktrees", "agent-1")
	git(t, primary, "worktree", "add", "-q", "-b", "agent-1", claude, "origin/develop")
	opt := Options{
		Protected: config.DefaultProtected, StaleDays: 7,
		Now: func() time.Time { return time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC) },
		GH:  fakeGH(map[string]string{"done": `{"state":"MERGED","mergedAt":"x","baseRefName":"develop","headRefOid":"` + git(t, done, "rev-parse", "HEAD") + `"}`}),
	}
	res, err := PlanSweep(ctx, []string{primary, primary}, opt)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, w := range res.Worktrees {
		states[w.Branch+w.Path] = w.State
		states[w.Branch] = w.State
	}
	want := map[string]string{"done": StateWouldFinish, "dirty": StateDirty, "unpushed": StateUnpushed, "stale": StateStale, "agent-1": StateActive}
	for b, st := range want {
		if states[b] != st {
			t.Errorf("%s = %q, want %q (%v)", b, states[b], st, res.Worktrees)
		}
	}
	if states["gone"+gone] != StateOrphaned || len(res.Repos) != 1 || len(res.Worktrees) != 6 {
		t.Fatalf("worktrees %+v repos %v", res.Worktrees, res.Repos)
	}
	if _, err := os.Stat(done); err != nil {
		t.Fatal("a dry run removed a worktree")
	}
	if err := ApplySweep(ctx, res, opt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Fatal("merged worktree remains")
	}
	for _, keep := range []string{dirty, unpushed, stale, claude} {
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("sweep removed %s", keep)
		}
	}
	if list := git(t, primary, "worktree", "list"); strings.Contains(list, gone) {
		t.Fatalf("prune left the orphan: %s", list)
	}
}

func TestReposForAllRepos(t *testing.T) {
	home, primary := fixture(t)
	s := Resolve(&config.Repo{}, &config.User{})
	addWorktree(t, s, primary, "feat/nested")
	other := filepath.Join(home, "src", "other")
	git(t, home, "init", "-q", other)
	s.Checkouts["other"] = other
	repos := s.Repos(t.Context())
	if len(repos) != 2 || repos[0] != primary || repos[1] != other {
		t.Fatalf("repos %v", repos)
	}
}
