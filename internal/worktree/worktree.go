// Package worktree manages the linked Git worktrees agentflow creates for tasks:
// one per task at <root>/<repo>/<branch>, a git-excluded record in each, an
// optional go.work, and cleanup once the task's pull request is merged.
package worktree

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gitx"
)

// RecordFile is the worktree record, relative to the worktree root.
const RecordFile = ".agentflow/worktree.yml"

// Settings are the resolved worktree options for one repository.
type Settings struct {
	// Root holds <root>/<repo>/<branch>.
	Root string
	// GoWork is nil when the repository writes no go.work.
	GoWork *config.GoWork
	// Checkouts maps repository names to primary checkouts.
	Checkouts map[string]string
	home      string
}

// Resolve combines the repository and personal settings. The repository root
// wins over the personal one; the default root is ~/.worktrees.
func Resolve(repo *config.Repo, user *config.User) Settings {
	home, _ := os.UserHomeDir()
	s := Settings{home: home, Checkouts: map[string]string{}}
	if user != nil {
		s.Root = user.Worktree.Root
		for name, dir := range user.Worktree.Checkouts {
			s.Checkouts[name] = expand(home, dir)
		}
	}
	if repo != nil {
		if repo.Worktree.Root != "" {
			s.Root = repo.Worktree.Root
		}
		s.GoWork = repo.Worktree.GoWork
	}
	if s.Root == "" {
		s.Root = filepath.Join(home, ".worktrees")
	}
	s.Root = expand(home, s.Root)
	return s
}

func expand(home, p string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// Path is the worktree directory for branch in repo.
func (s Settings) Path(repo, branch string) string {
	return filepath.Join(s.Root, repo, filepath.FromSlash(branch))
}

// Checkout is the primary checkout of repo: worktree.checkouts.<repo>, or ~/GitHub/<repo>.
func (s Settings) Checkout(repo string) string {
	if dir := s.Checkouts[repo]; dir != "" {
		return dir
	}
	return filepath.Join(s.home, "GitHub", repo)
}

// RepoName names the repository: the last path component of origin, or the
// directory of the primary checkout.
func RepoName(ctx context.Context, repo *gitx.Repo) string {
	if raw, err := repo.Git(ctx, "remote", "get-url", "origin"); err == nil {
		if remote, err := gitx.ParseRemoteURL(raw); err == nil && filepath.IsLocal(remote.Name) && filepath.Base(remote.Name) == remote.Name {
			return remote.Name
		}
	}
	if common := repo.GitDir(ctx); filepath.Base(common) == ".git" {
		return filepath.Base(filepath.Dir(common))
	}
	return filepath.Base(repo.Dir)
}

// Record describes a task worktree. It is written to RecordFile and git-excluded.
type Record struct {
	Issue   string    `yaml:"issue"`
	TaskRef string    `yaml:"task_ref,omitempty"`
	Branch  string    `yaml:"branch"`
	Base    string    `yaml:"base"`
	BaseSHA string    `yaml:"base_sha,omitempty"`
	Repo    string    `yaml:"repo"`
	Created time.Time `yaml:"created"`
}

// WriteRecord writes the record into the worktree at dir and excludes it from Git.
func WriteRecord(ctx context.Context, dir string, r Record) error {
	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, filepath.FromSlash(RecordFile))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	header := "# Written by `agentflow start --worktree`. Not committed (listed in .git/info/exclude).\n"
	if err := os.WriteFile(path, append([]byte(header), b...), 0o644); err != nil {
		return err
	}
	return Exclude(ctx, dir, "/"+RecordFile)
}

// ReadRecord reads the record of the worktree at dir.
func ReadRecord(dir string) (Record, error) {
	var r Record
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(RecordFile)))
	if err != nil {
		return r, err
	}
	return r, yaml.Unmarshal(b, &r)
}

// Exclude adds patterns to the repository's info/exclude file, once each.
// Linked worktrees share the file with the primary checkout.
func Exclude(ctx context.Context, dir string, patterns ...string) error {
	repo := &gitx.Repo{Dir: dir}
	out, err := repo.Git(ctx, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	path := strings.TrimSpace(out)
	current, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(string(current), "\n")
	var add []string
	for _, p := range patterns {
		if !slices.Contains(lines, p) {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return nil
	}
	text := string(current)
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += strings.Join(add, "\n") + "\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// Entry is one record of `git worktree list --porcelain`.
type Entry struct {
	Path     string
	Head     string
	Branch   string // short name; "" when detached
	Bare     bool
	Detached bool
	Prunable bool
	Primary  bool // the main worktree, listed first
}

// List returns the worktrees of the repository containing dir, primary first.
func List(ctx context.Context, dir string) ([]Entry, error) {
	out, err := (&gitx.Repo{Dir: dir}).Git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var e Entry
		for _, line := range strings.Split(block, "\n") {
			key, value, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				e.Path = value
			case "HEAD":
				e.Head = value
			case "branch":
				e.Branch = strings.TrimPrefix(value, "refs/heads/")
			case "bare":
				e.Bare = true
			case "detached":
				e.Detached = true
			case "prunable":
				e.Prunable = true
			}
		}
		if e.Path == "" {
			continue
		}
		e.Primary = len(entries) == 0
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("git worktree list returned no worktrees for %s", dir)
	}
	return entries, nil
}

// Primary returns the primary checkout of the repository containing dir.
func Primary(ctx context.Context, dir string) (string, error) {
	entries, err := List(ctx, dir)
	if err != nil {
		return "", err
	}
	return entries[0].Path, nil
}

// IsLinked reports whether dir is inside a linked worktree rather than the primary checkout.
func IsLinked(ctx context.Context, dir string) bool {
	repo := &gitx.Repo{Dir: dir}
	gitDir, err1 := repo.Git(ctx, "rev-parse", "--path-format=absolute", "--git-dir")
	common, err2 := repo.Git(ctx, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err1 != nil || err2 != nil {
		return false
	}
	return filepath.Clean(strings.TrimSpace(gitDir)) != filepath.Clean(strings.TrimSpace(common))
}
