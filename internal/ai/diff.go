package ai

import (
	"context"
	"errors"

	"github.com/ryanhelms/agentflow/internal/app"
)

// DiffFlags are the flags GitDiff passes before the caller's: plain output
// in the a/ and b/ form Filter reads whatever the user's git settings say
// (diff.noprefix, diff.mnemonicPrefix, color, external diff and textconv
// drivers), and no program of the repository's configuration run.
var DiffFlags = []string{"--no-color", "--no-ext-diff", "--no-textconv", "--src-prefix=a/", "--dst-prefix=b/"}

// GitDiff runs git diff in a's worktree with DiffFlags and then args (for
// example "--cached", or a range and "--" and paths). Gatherers make every
// diff part with it: Filter leaves out the content of a file whose header
// does not have the a/ and b/ prefixes.
func GitDiff(ctx context.Context, a *app.App, args ...string) (string, error) {
	if a == nil || a.Repo == nil {
		return "", errors.New("ai: diff: no worktree")
	}
	return a.Repo.Git(ctx, append([]string{"diff", DiffFlags[0], DiffFlags[1], DiffFlags[2], DiffFlags[3], DiffFlags[4]}, args...)...)
}
