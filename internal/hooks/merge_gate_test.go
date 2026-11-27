package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ryanhelms/agentflow/internal/boundary"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// fakeBases answers `gh pr view N [--repo R]` from bases[R][N], with R "" when
// the command names no repository. It records every call and its folder.
func fakeBases(t *testing.T, bases map[string]map[string]string) *[]string {
	t.Helper()
	var asked []string
	MergeGH = func(_ context.Context, dir string, args ...string) (string, error) {
		asked = append(asked, dir+": "+strings.Join(args, " "))
		repo := ""
		if i := slices.Index(args, "--repo"); i >= 0 && i+1 < len(args) {
			repo = args[i+1]
		}
		if len(args) > 2 {
			if base, ok := bases[repo][args[2]]; ok {
				return base + "\n", nil
			}
		}
		return "", errors.New("no PR")
	}
	t.Cleanup(func() { MergeGH = worktree.ExecGH })
	return &asked
}

func preBashIn(t *testing.T, cmd, cwd string) (int, string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"cwd": cwd, "tool_input": map[string]string{"command": cmd}})
	var stderr bytes.Buffer
	return exitCode(PreBash(t.Context(), bytes.NewReader(b), &stderr, false)), stderr.String()
}

// A second command on another line, or inside <(...), is never judged by the
// first merge's base.
func TestPreBashMergeGateRefusesASecondCommand(t *testing.T) {
	home, _, _ := twoRepos(t)
	fakeBases(t, map[string]map[string]string{
		"":                          {"12": "develop", "13": "main"},
		"ryanhelms/agent-commander": {"12": "develop", "13": "main"},
	})
	for _, cmd := range []string{
		"gh pr merge 12 --squash\ngh pr merge 13 --merge",
		"gh pr merge 12 --squash\n\ngh pr merge 13 --merge",
		"gh pr merge 12 --squash\rgh pr merge 13 --merge",
		"gh pr merge 12 --squash --body-file <(gh pr merge 13 --merge)",
		"gh pr merge 12 --squash --body-file >(gh pr merge 13 --merge)",
	} {
		if got, stderr := preBashIn(t, cmd, home); got != 2 || !strings.Contains(stderr, "only a single gh pr merge") {
			t.Errorf("%q: exit %d: %s", cmd, got, stderr)
		}
	}
	// The coordinator's own forms stay allowed.
	for _, cmd := range []string{
		"gh pr merge 12 --squash --body 'first line\n\nsecond line'",
		"gh pr merge 12 --squash --body \"first line\nsecond line\"",
		"gh pr merge 12 --squash --repo ryanhelms/agent-commander",
		"gh pr merge 12 --squash -R ryanhelms/agent-commander --delete-branch",
		"gh pr merge 12 --squash --subject 'feat(PLAT-1): add x'",
		"gh pr merge 12 --squash",
	} {
		if got, stderr := preBashIn(t, cmd, home); got != 0 {
			t.Errorf("%q: exit %d: %s", cmd, got, stderr)
		}
	}
}

// homeWithoutSlug starts a session in a repository named agent-commander with
// no GitHub remote: the repository boundary then compares only the name, so
// -R someone/agent-commander reaches the merge gate although it is another
// repository.
func homeWithoutSlug(t *testing.T) string {
	t.Helper()
	gitEnv(t)
	home := filepath.Join(t.TempDir(), "agent-commander")
	if err := os.MkdirAll(filepath.Join(home, ".agentflow"), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, home, "init", "-q", "-b", "develop")
	runGit(t, home, "commit", "-q", "--allow-empty", "-m", "init")
	if err := os.WriteFile(filepath.Join(home, ".agentflow", "config.yml"), []byte("policy: {protected_branches: [main, develop], coordinator_merge: true}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", home)
	t.Setenv(boundary.SessionEnv, "")
	t.Setenv("GH_REPO", "")
	t.Chdir(home)
	return home
}

// A merge that names another repository is judged by that repository's PR,
// never by the PR with the same number here.
func TestPreBashMergeGateUsesTheNamedRepository(t *testing.T) {
	home := homeWithoutSlug(t)
	const other = "someone/agent-commander"
	asked := fakeBases(t, map[string]map[string]string{
		"":    {"12": "develop", "13": "main"}, // this repository
		other: {"12": "main", "13": "develop"},
	})
	for _, cmd := range []string{
		"gh pr merge 12 --squash -R " + other,
		"gh pr merge 12 --squash -R" + other,
		"gh pr merge 12 --squash --repo " + other,
		"gh pr merge 12 --squash --repo=" + other,
		"gh pr merge -R " + other + " --squash 12",
		"GH_REPO=" + other + " gh pr merge 12 --squash",
	} {
		*asked = nil
		got, stderr := preBashIn(t, cmd, home)
		if got != 2 || !strings.Contains(stderr, "merging into main is a release decision") {
			t.Errorf("%q: exit %d: %s", cmd, got, stderr)
		}
		if !slices.ContainsFunc(*asked, func(a string) bool { return strings.Contains(a, "pr view 12 --repo "+other) }) {
			t.Errorf("%q: looked up %v", cmd, *asked)
		}
	}
	// Forms that would make gh merge another PR than the one looked up.
	for _, cmd := range []string{
		"GH_REPO=" + other + " env -u GH_REPO gh pr merge 13 --squash", // merges 13 here, into main
		"gh pr merge 13 --squash -R " + other + " -R " + other + "x",
	} {
		if got, stderr := preBashIn(t, cmd, home); got != 2 {
			t.Errorf("%q: exit %d: %s", cmd, got, stderr)
		}
	}
	if got, stderr := preBashIn(t, "gh pr merge 13 --squash -R "+other, home); got != 0 {
		t.Errorf("merge of the other repository's develop PR: exit %d: %s", got, stderr)
	}
}

// Words that look like -R, or grouped and unknown flags, never steer the
// lookup to another PR or repository than the one gh merges.
func TestPreBashMergeGateParsesLikeGH(t *testing.T) {
	home, wt, _ := twoRepos(t)
	asked := fakeBases(t, map[string]map[string]string{
		"":                          {"12": "develop", "13": "main"},
		"ryanhelms/agent-commander": {"12": "develop", "13": "main"},
		"ryanhelms/agentflow":       {"12": "main", "13": "develop"},
	})
	for _, cmd := range []string{
		// a flag value that looks like -R
		"gh pr merge 13 --squash --body -Rryanhelms/agentflow",
		"gh pr merge 13 --squash --body '-Rryanhelms/agentflow'",
		// gh uses the last -R; the lookup must not use the first
		"gh pr merge 13 --squash -R ryanhelms/agentflow -R ryanhelms/agent-commander",
		// grouped short flags: -b takes 12, so gh merges 13
		"gh pr merge -sb 12 13",
		// -sR names another repository, whose PR 12 targets main
		"gh pr merge 12 -sR ryanhelms/agentflow",
		"gh pr merge 12 -sRryanhelms/agentflow",
		// spellings the gh pr merge pattern does not match
		"gh -R ryanhelms/agent-commander pr merge 13",
		"gh pr -R ryanhelms/agent-commander merge 13",
		"gh pr --repo=ryanhelms/agent-commander merge 13",
		"gh 'pr' merge 13",
		"gh pr mer''ge 13",
		`g\h pr merge 13`,
		"/usr/local/bin/gh pr \"merge\" 13",
		"sudo -u me gh -R ryanhelms/agent-commander pr merge 13",
		"cat <(gh -R ryanhelms/agent-commander pr merge 13)",
		"bash -c 'gh pr merge 13'",
		// merges through the API
		"gh api -X PUT repos/ryanhelms/agent-commander/pulls/13/merge",
		"gh api --method PUT /repos/{owner}/{repo}/pulls/13/merge -f merge_method=squash",
		`gh api graphql -f query='mutation { mergePullRequest(input: {pullRequestId: "PR_1"}) { clientMutationId } }'`,
		// unknown flags and extra arguments
		"gh pr merge 12 --squash --unknown 13",
		"gh pr merge 12 13",
		"gh pr merge 12 -- 13",
	} {
		if got, stderr := preBashIn(t, cmd, home); got != 2 {
			t.Errorf("%q: exit %d: %s", cmd, got, stderr)
		}
	}
	// gh pr merge without a number merges the branch of the command's folder.
	*asked = nil
	MergeGH = func(_ context.Context, dir string, args ...string) (string, error) {
		*asked = append(*asked, dir)
		if dir == wt {
			return "main\n", nil
		}
		return "develop\n", nil
	}
	if got, stderr := preBashIn(t, "gh pr merge --squash", wt); got != 2 || !slices.Equal(*asked, []string{wt}) {
		t.Errorf("merge without a number in %s: exit %d, looked up in %v: %s", wt, got, *asked, stderr)
	}
}

// Without policy.coordinator_merge every spelling of a merge is refused.
func TestPreBashRefusesEveryMergeSpelling(t *testing.T) {
	home, _, _ := twoRepos(t)
	if err := os.WriteFile(filepath.Join(home, ".agentflow", "config.yml"), []byte("policy: {protected_branches: [main, develop]}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []string{
		"gh pr merge 13",
		"gh -R ryanhelms/agent-commander pr merge 13",
		"gh pr -R ryanhelms/agent-commander merge 13",
		"gh 'pr' merge 13",
		"cat <(gh pr -R ryanhelms/agent-commander merge 13)",
		"gh api -X PUT repos/ryanhelms/agent-commander/pulls/13/merge",
		"=gh -R ryanhelms/agent-commander pr merge 13",
		"$GH -R ryanhelms/agent-commander pr merge 13",
		"$(which gh) pr -R ryanhelms/agent-commander merge 13",
	} {
		if got, stderr := preBashIn(t, cmd, home); got != 2 || !strings.Contains(stderr, "human decision") {
			t.Errorf("%q: exit %d: %s", cmd, got, stderr)
		}
	}
	// Asking whether a PR is merged is a read.
	if got, stderr := preBashIn(t, "gh api repos/ryanhelms/agent-commander/pulls/13/merge", home); got != 0 {
		t.Errorf("GET of the merge endpoint: exit %d: %s", got, stderr)
	}
}

// gh merges the PR a URL names even when -R or GH_REPO names this repository,
// so the URL decides, in every mode.
func TestPreBashMergeURLOfAnotherRepository(t *testing.T) {
	home, _, _ := twoRepos(t)
	fakeBases(t, map[string]map[string]string{"": {"https://github.com/ryanhelms/agentflow/pull/13": "develop"}})
	for _, conventionOnly := range []bool{false, true} {
		for _, cmd := range []string{
			"gh pr merge https://github.com/ryanhelms/agentflow/pull/13 --squash -R ryanhelms/agent-commander",
			"GH_REPO=ryanhelms/agent-commander gh pr merge https://github.com/ryanhelms/agentflow/pull/13 --squash",
		} {
			b, _ := json.Marshal(map[string]any{"cwd": home, "tool_input": map[string]string{"command": cmd}})
			var stderr bytes.Buffer
			if got := exitCode(PreBash(t.Context(), bytes.NewReader(b), &stderr, conventionOnly)); got != 2 || !strings.Contains(stderr.String(), "agentflow delegate") {
				t.Errorf("convention-only=%v %q: exit %d: %s", conventionOnly, cmd, got, stderr.String())
			}
		}
	}
}

func TestParseMerge(t *testing.T) {
	t.Setenv("GH_REPO", "")
	for cmd, want := range map[string]mergeCommand{
		"gh pr merge 12 --squash":                                     {pr: "12"},
		"gh pr merge --squash -t \"feat: x\" feat-1":                  {pr: "feat-1"},
		"gh pr merge --auto --squash":                                 {},
		"gh pr merge https://github.com/o/r/pull/3":                   {pr: "https://github.com/o/r/pull/3"},
		"gh pr merge https://github.com/O/R/pull/3 -R github.com/o/r": {pr: "https://github.com/O/R/pull/3", repo: "github.com/o/r"},
		"gh pr merge 5 --body -Ro/x -R o/r":                           {pr: "5", repo: "o/r"},
		"gh pr merge --subject='feat: x' --repo=o/r -- 5":             {pr: "5", repo: "o/r"},
		"GH_REPO=o/r gh pr merge 5":                                   {pr: "5", repo: "o/r"},
		"GH_REPO=o/r gh pr merge 5 -R O/R -R o/r":                     {pr: "5", repo: "O/R"},
		"gh pr merge --match-head-commit abc --author-email a@b 5":    {pr: "5"},
	} {
		got, err := parseMerge(cmd)
		if err != nil || got != want {
			t.Errorf("parseMerge(%q) = %+v, %v want %+v", cmd, got, err, want)
		}
	}
	for _, cmd := range []string{
		"cd x && gh pr merge 5", "gh pr merge 5 -sR o/r", "gh pr merge -sb 5 6", "gh pr merge 5 --body",
		"gh pr merge 5 -R o/r -R o/s", "env gh pr merge 5", "gh -R o/r pr merge 5", "gh pr merge 5 6",
		"gh pr merge https://github.com/o/x/pull/5 -R o/r", "GH_REPO=o/r gh pr merge https://github.com/o/x/pull/5",
	} {
		if got, err := parseMerge(cmd); err == nil {
			t.Errorf("parseMerge(%q) = %+v, want an error", cmd, got)
		}
	}
}
