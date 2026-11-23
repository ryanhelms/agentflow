package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/nscaledev/devflow/internal/provider"
	"github.com/nscaledev/devflow/internal/provider/filesystem"
)

// The object database and index are private to one operation. Using blobs
// directly avoids invoking checkout filters and preserves Markdown revision
// hashes across platforms, regardless of attributes or autocrlf settings.
type checkout struct {
	root, origin, branch string
	okfKnowledge         bool
}

func (c checkout) git(ctx context.Context, input []byte, args ...string) (string, error) {
	flags := []string{"--literal-pathspecs", "-c", "core.protectHFS=true", "-c", "core.protectNTFS=true", "-c", "core.hooksPath=" + os.DevNull,
		"-c", "user.name=devflow", "-c", "user.email=devflow@localhost"}
	cmd := exec.CommandContext(ctx, "git", append(flags, args...)...)
	cmd.Dir = c.root
	// A hook may launch devflow with GIT_DIR or GIT_INDEX_FILE pointing
	// at the engineer's checkout. Never inherit Git's repository overrides.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_ALLOW_PROTOCOL=file:https:http:ssh:git", "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

func (c *checkout) init(ctx context.Context) error {
	if _, err := c.git(ctx, nil, "init", "--bare", "--template=", "."); err != nil {
		return err
	}
	if c.branch == "" {
		out, err := c.git(ctx, nil, "ls-remote", "--symref", "--", c.origin, "HEAD")
		if err != nil {
			return err
		}
		for line := range strings.Lines(out) {
			ref, name, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "\t")
			if name == "HEAD" && strings.HasPrefix(ref, "ref: refs/heads/") {
				c.branch = strings.TrimPrefix(ref, "ref: refs/heads/")
			}
		}
	}
	if c.branch == "" || strings.HasPrefix(c.branch, "-") {
		return errors.New("expected a repository branch name")
	}
	_, err := c.git(ctx, nil, "check-ref-format", "refs/heads/"+c.branch)
	return err
}

func (c checkout) fetch(ctx context.Context, branch string) (string, error) {
	if _, err := c.git(ctx, nil, "fetch", "--no-tags", "--no-recurse-submodules", "--", c.origin, "refs/heads/"+branch); err != nil {
		return "", err
	}
	out, err := c.git(ctx, nil, "rev-parse", "--verify", "FETCH_HEAD^{commit}")
	return strings.TrimSpace(out), err
}

// Git can rewrite even literal URLs through insteadOf and pushInsteadOf.
// Inspect Git's effective push destination in this private checkout before
// asking the caller to approve the configured origin.
func (c checkout) checkPushOrigin(ctx context.Context) error {
	if _, err := c.git(ctx, nil, "config", "--local", "remote.devflow-approval.url", c.origin); err != nil {
		return err
	}
	pushURL, err := c.git(ctx, nil, "remote", "get-url", "--push", "--all", "devflow-approval")
	if err != nil {
		return err
	}
	if strings.TrimSuffix(pushURL, "\n") != c.origin {
		return errors.New("git rewrites the repository push destination; configure the resolved repository URL for approval")
	}
	return nil
}

func (c checkout) push(ctx context.Context, commit, branch string) error {
	_, err := c.git(ctx, nil, "push", "--no-follow-tags", "--recurse-submodules=no", "--", c.origin, commit+":refs/heads/"+branch)
	return err
}

type entry struct {
	mode, oid, name string
}

func (c checkout) entries(ctx context.Context, tree string, recursive bool, names ...string) ([]entry, error) {
	args := []string{"ls-tree", "-z"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, tree, "--")
	out, err := c.git(ctx, nil, append(args, names...)...)
	if err != nil {
		return nil, err
	}
	var entries []entry
	for item := range strings.SplitSeq(strings.TrimSuffix(out, "\x00"), "\x00") {
		if item == "" {
			continue
		}
		header, name, ok := strings.Cut(item, "\t")
		fields := strings.Fields(header)
		if !ok || len(fields) != 3 {
			return nil, errors.New("invalid git tree entry")
		}
		entries = append(entries, entry{mode: fields[0], oid: fields[2], name: name})
	}
	return entries, nil
}

type snapshot struct {
	dir   string
	modes map[string]string
}

func (c checkout) load(ctx context.Context, base, directory string) (snapshot, error) {
	dir, err := os.MkdirTemp(c.root, "records-")
	if err != nil {
		return snapshot{}, err
	}
	s := snapshot{dir, make(map[string]string)}
	tree := base
	for _, part := range strings.Split(directory, "/") {
		if part == "." {
			continue
		}
		entries, err := c.entries(ctx, tree, false, part)
		if err != nil {
			return snapshot{}, err
		}
		if len(entries) == 0 {
			return s, nil // Git cannot track an empty directory.
		}
		if len(entries) != 1 || entries[0].name != part || entries[0].mode != "040000" {
			return snapshot{}, fmt.Errorf("repository path component %q must be a directory", part)
		}
		tree = entries[0].oid
	}
	entries, err := c.entries(ctx, tree, c.okfKnowledge)
	if err != nil {
		return snapshot{}, err
	}
	dirs := make(map[string]bool)
	for _, e := range entries {
		regular := e.mode == "100644" || e.mode == "100755"
		if c.okfKnowledge {
			if !regular || !filesystem.IsKnowledgeCandidate(e.name) {
				continue
			}
		} else if !strings.EqualFold(path.Ext(e.name), ".md") {
			continue
		}
		if !regular || !safeComponent(path.Base(e.name)) {
			return snapshot{}, fmt.Errorf("expected a regular Markdown file at %q", e.name)
		}
		if err := s.makeParents(e.name, dirs); err != nil {
			return snapshot{}, err
		}
		body, err := c.git(ctx, nil, "cat-file", "blob", e.oid)
		if err != nil {
			return snapshot{}, err
		}
		f, err := os.OpenFile(filepath.Join(s.dir, e.name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return snapshot{}, err
		}
		_, err = f.WriteString(body)
		if err := errors.Join(err, f.Close()); err != nil {
			return snapshot{}, err
		}
		// Only root files are eligible v1 write destinations. Never match a
		// nested blob merely because it has the same bytes as the write.
		if !strings.Contains(e.name, "/") {
			s.modes[e.name] = e.mode
		}
	}
	return s, nil
}

// Materialize only directories containing candidates. Mkdir (not MkdirAll)
// detects paths which alias a different Git spelling on this filesystem.
func (s snapshot) makeParents(name string, dirs map[string]bool) error {
	parent := ""
	parts := strings.Split(name, "/")
	for _, part := range parts[:len(parts)-1] {
		if !safeComponent(part) {
			return fmt.Errorf("invalid knowledge directory component %q", part)
		}
		parent = path.Join(parent, part)
		if !dirs[parent] {
			if err := os.Mkdir(filepath.Join(s.dir, parent), 0700); err != nil {
				return err
			}
			dirs[parent] = true
		}
	}
	return nil
}

// FileSystem returns the SHA-256 of the exact bytes it wrote. Match that
// receipt to a single file, including records renamed by an engineer.
func (s snapshot) changedFile(revision string) (string, []byte, error) {
	files, err := os.ReadDir(s.dir)
	if err != nil {
		return "", nil, err
	}
	// Use existing Git spellings first. A filesystem may return a different
	// Unicode normalization from ReadDir for the very same file.
	var names []string
	for name := range s.modes {
		names = append(names, name)
	}
	for _, f := range files {
		if _, known := s.modes[f.Name()]; !known && f.Type().IsRegular() && strings.EqualFold(path.Ext(f.Name()), ".md") {
			names = append(names, f.Name())
		}
	}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(s.dir, name))
		if err != nil {
			return "", nil, err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) == revision {
			return name, data, nil
		}
	}
	return "", nil, errors.New("changed Markdown file does not match the write revision")
}

func (c checkout) commit(ctx context.Context, base, directory, operation string, s snapshot, result provider.WriteResult) (string, error) {
	name, body, err := s.changedFile(result.Revision)
	if err != nil {
		return "", err
	}
	repoPath := path.Join(directory, name)
	// The snapshot omits symlinks and gitlinks. An exclusive filesystem
	// create must also be exclusive in the original Git tree.
	if s.modes[name] == "" {
		entries, err := c.entries(ctx, base, false, repoPath)
		if err != nil {
			return "", err
		}
		if len(entries) != 0 {
			return "", fmt.Errorf("repository destination %q is occupied: %w", repoPath, provider.ErrConflict)
		}
	}
	blob, err := c.git(ctx, body, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", err
	}
	mode := s.modes[name]
	if mode == "" {
		mode = "100644"
	}
	if _, err := c.git(ctx, nil, "read-tree", base); err != nil {
		return "", err
	}
	if _, err := c.git(ctx, nil, "update-index", "--add", "--cacheinfo", mode, strings.TrimSpace(blob), repoPath); err != nil {
		return "", err
	}
	tree, err := c.git(ctx, nil, "write-tree")
	if err != nil {
		return "", err
	}
	tree = strings.TrimSpace(tree)
	changed, err := c.git(ctx, nil, "diff-tree", "--no-renames", "--no-ext-diff", "-r", "-z", "--name-only", base, tree)
	if err != nil {
		return "", err
	}
	if changed != repoPath+"\x00" {
		return "", fmt.Errorf("expected only %q to change: %w", repoPath, provider.ErrConflict)
	}
	msg := "chore: update provider record\n\nDevflow-Operation: " + operation
	commit, err := c.git(ctx, nil, "commit-tree", "--no-gpg-sign", tree, "-p", base, "-m", msg)
	return strings.TrimSpace(commit), err
}
