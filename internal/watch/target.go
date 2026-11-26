package watch

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// NewTarget describes the worktree checked out at root. It asks git for
// the worktree's own git dir and the repository's common dir
// (git rev-parse --git-dir --git-common-dir), which differ for a linked
// worktree: its HEAD and index live in <common>/worktrees/<name>, its refs
// in <common>/refs. Paths are absolute with symbolic links resolved, the
// form the Sources report.
func NewTarget(ctx context.Context, repo bus.RepoID, worktree bus.WorktreeID, root string) (Target, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return Target{}, fmt.Errorf("worktree root %s: %w", root, err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", abs, "rev-parse", "--show-toplevel", "--git-dir", "--git-common-dir")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return Target{}, fmt.Errorf("git rev-parse in %s: %w: %s", abs, err, strings.TrimSpace(stderr.String()))
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return Target{}, fmt.Errorf("git rev-parse in %s: unexpected output %q", abs, out)
	}
	t := Target{Repo: repo, Worktree: worktree}
	for i, dst := range []*string{&t.Root, &t.GitDir, &t.CommonDir} {
		p := strings.TrimSpace(lines[i])
		if !filepath.IsAbs(p) {
			p = filepath.Join(abs, p) // older git prints paths relative to -C
		}
		*dst = p
	}
	return canonical(t)
}

// canonical makes t's paths absolute, clean and free of symbolic links, so
// that paths reported by a Source compare equal to them (macOS, for
// example, reports /private/var for /var).
func canonical(t Target) (Target, error) {
	for _, p := range []*string{&t.Root, &t.GitDir, &t.CommonDir} {
		if *p == "" {
			continue
		}
		abs, err := filepath.Abs(*p)
		if err != nil {
			return Target{}, fmt.Errorf("resolve %s: %w", *p, err)
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		*p = filepath.Clean(abs)
	}
	return t, nil
}
