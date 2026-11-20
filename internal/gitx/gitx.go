// Package gitx wraps the git CLI for the few operations devflow needs.
package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// KeyRe matches a Jira issue key such as PLAT-123.
var KeyRe = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-[0-9]+\b`)

// Repo is a git working tree rooted at Dir.
type Repo struct {
	Dir string
}

// Open finds the repository containing dir.
func Open(ctx context.Context, dir string) (*Repo, error) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}
	return &Repo{Dir: strings.TrimSpace(out)}, nil
}

func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Git runs an arbitrary git command in the repo and returns stdout.
func (r *Repo) Git(ctx context.Context, args ...string) (string, error) {
	return run(ctx, r.Dir, args...)
}

// Branch returns the current branch name, or "" when detached.
func (r *Repo) Branch(ctx context.Context) string {
	out, err := r.Git(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// DefaultBranch returns origin's default branch, falling back to main.
func (r *Repo) DefaultBranch(ctx context.Context) string {
	out, err := r.Git(ctx, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err == nil {
		return strings.TrimPrefix(strings.TrimSpace(out), "origin/")
	}
	return "main"
}

// KeyFromBranch extracts the first Jira key from a branch name.
func KeyFromBranch(branch string) string {
	return KeyRe.FindString(strings.ToUpper(branch))
}

// Change is one path in the working tree with its git status letter.
type Change struct {
	Status string // A, M, D, R, ? (untracked)
	Path   string
	Staged bool
}

// Changes lists staged and unstaged changes, including untracked files.
func (r *Repo) Changes(ctx context.Context) ([]Change, error) {
	out, err := r.Git(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var changes []Change
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		x, y, path := e[0], e[1], e[3:]
		if x == 'R' || x == 'C' {
			i++ // the next entry is the rename source
		}
		switch {
		case x == '?' && y == '?':
			changes = append(changes, Change{Status: "?", Path: path})
		case x != ' ':
			changes = append(changes, Change{Status: string(x), Path: path, Staged: true})
		default:
			changes = append(changes, Change{Status: string(y), Path: path})
		}
	}
	return changes, nil
}

// StagedFiles lists paths staged for commit.
func (r *Repo) StagedFiles(ctx context.Context) ([]string, error) {
	out, err := r.Git(ctx, "diff", "--cached", "--name-only", "-z")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// StagedDiff returns the staged diff, truncated to max bytes.
func (r *Repo) StagedDiff(ctx context.Context, max int) (string, bool, error) {
	out, err := r.Git(ctx, "diff", "--cached", "--no-color", "--unified=2")
	if err != nil {
		return "", false, err
	}
	if max > 0 && len(out) > max {
		return out[:max], true, nil
	}
	return out, false, nil
}

// DiffStat returns `git diff --cached --stat` output.
func (r *Repo) DiffStat(ctx context.Context) string {
	out, _ := r.Git(ctx, "diff", "--cached", "--stat", "--no-color")
	return strings.TrimSpace(out)
}

// Messages returns full commit messages for a revision range, oldest first.
func (r *Repo) Messages(ctx context.Context, rng string) ([]string, error) {
	out, err := r.Git(ctx, "log", "--reverse", "--format=%B%x00", rng)
	if err != nil {
		return nil, err
	}
	var msgs []string
	for _, m := range strings.Split(out, "\x00") {
		if m = strings.TrimSpace(m); m != "" {
			msgs = append(msgs, m)
		}
	}
	return msgs, nil
}

// Unpushed returns messages of commits on HEAD not yet on its upstream (or the default branch).
func (r *Repo) Unpushed(ctx context.Context) ([]string, error) {
	base := "@{upstream}"
	if _, err := r.Git(ctx, "rev-parse", "--verify", "--quiet", base); err != nil {
		base = "origin/" + r.DefaultBranch(ctx)
		if _, err := r.Git(ctx, "rev-parse", "--verify", "--quiet", base); err != nil {
			return nil, nil
		}
	}
	return r.Messages(ctx, base+"..HEAD")
}

// Add stages paths.
func (r *Repo) Add(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	_, err := r.Git(ctx, append([]string{"add", "--"}, paths...)...)
	return err
}

// Commit commits the index with message. If paths is non-empty, only those paths are committed.
func (r *Repo) Commit(ctx context.Context, message string, paths []string) (string, error) {
	args := []string{"commit", "--file=-"}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Stdin = strings.NewReader(message)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	sha, err := r.Git(ctx, "rev-parse", "--short", "HEAD")
	return strings.TrimSpace(sha), err
}

// UserEmail returns the configured git user.email.
func (r *Repo) UserEmail(ctx context.Context) string {
	out, _ := r.Git(ctx, "config", "user.email")
	return strings.TrimSpace(out)
}

// RemoteRepo returns "owner/name" for origin, if it is a GitHub remote.
func (r *Repo) RemoteRepo(ctx context.Context) string {
	out, err := r.Git(ctx, "remote", "get-url", "origin")
	if err != nil {
		return ""
	}
	return ParseGitHubRemote(strings.TrimSpace(out))
}

var remoteRe = regexp.MustCompile(`github\.com[:/]([^/]+)/([^/]+?)(?:\.git)?/?$`)

// ParseGitHubRemote turns a GitHub remote URL into "owner/name".
func ParseGitHubRemote(url string) string {
	m := remoteRe.FindStringSubmatch(url)
	if m == nil {
		return ""
	}
	return m[1] + "/" + m[2]
}

// HasUncommitted reports whether the working tree has any changes.
func (r *Repo) HasUncommitted(ctx context.Context) bool {
	out, _ := r.Git(ctx, "status", "--porcelain")
	return strings.TrimSpace(out) != ""
}

// CreateBranch creates and switches to a new branch from start.
func (r *Repo) CreateBranch(ctx context.Context, name, start string) error {
	_, err := r.Git(ctx, "switch", "-c", name, start)
	return err
}

// AddWorktree creates a worktree for a new branch next to the repo.
func (r *Repo) AddWorktree(ctx context.Context, path, branch, start string) error {
	_, err := r.Git(ctx, "worktree", "add", "-b", branch, path, start)
	return err
}

// GitDir returns the absolute path of the repository's common .git directory.
func (r *Repo) GitDir(ctx context.Context) string {
	out, err := r.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return r.Dir + "/.git"
	}
	return strings.TrimSpace(out)
}
