package boundary

import (
	"slices"
	"testing"
)

func TestRunsGHMerge(t *testing.T) {
	for cmd, want := range map[string]bool{
		"gh pr merge 5":                         true,
		"gh -R o/r pr merge 5":                  true,
		"gh pr --repo=o/r merge 5":              true,
		"gh 'pr' \"merge\" 5":                   true,
		`g\h pr merge 5`:                        true,
		"sudo -u me gh pr merge 5":              true,
		"echo x | xargs gh pr merge":            true,
		"cat <(gh pr merge 5)":                  true,
		"bash -c 'gh pr -R o/r merge 5'":        true,
		"gh api -X PUT repos/o/r/pulls/5/merge": true,
		"gh api graphql -f query='mutation{mergePullRequest(input:{})}'": true,
		"gh pr view 5":             false,
		"gh pr merges":             false,
		"gh api repos/o/r/pulls/5": false,
		"git merge main":           false,
		"git commit -m 'docs: explain gh pr merge'": false,
	} {
		if got := RunsGHMerge(cmd); got != want {
			t.Errorf("RunsGHMerge(%q) = %v want %v", cmd, got, want)
		}
	}
}

func TestWords(t *testing.T) {
	if got, ok := Words(`GH_REPO=o/r gh pr 'mer'"ge" 5 -t 'feat(x): y'`); !ok || !slices.Equal(got, []string{"GH_REPO=o/r", "gh", "pr", "merge", "5", "-t", "feat(x): y"}) {
		t.Errorf("Words = %q %v", got, ok)
	}
	for _, cmd := range []string{"a; b", "a\nb", "a && b", "a | b", "a > f", "a <(b)", "a $(b)", "a `b`", "(a)"} {
		if _, ok := Words(cmd); ok {
			t.Errorf("Words(%q) ok, want not a single command", cmd)
		}
	}
}

func TestProcessSubstitutionIsACommand(t *testing.T) {
	got := SimpleCommands("diff <(git show a:f) >(gh pr close 3)")
	if !slices.ContainsFunc(got, func(w []string) bool { return slices.Equal(w, []string{"gh", "pr", "close", "3"}) }) {
		t.Fatalf("SimpleCommands = %q", got)
	}
}
