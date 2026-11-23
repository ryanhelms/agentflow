// Package gitx prepares exact commit trees without changing the checkout's index.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// CommitTree identifies exactly what a local commit would record.
type CommitTree struct {
	Repository string
	GitDir     string
	Ref        string
	Parent     string
	Parents    string
	Tree       string
	Files      []string
	Diff       string
}

type commitIndex struct {
	repo           *Repo
	dir            string
	env            []string
	full, selected string
}

func (c *commitIndex) git(ctx context.Context, index string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = c.repo.Dir
	cmd.Env = append(append([]string{}, c.env...), "GIT_INDEX_FILE="+index)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return out.String(), nil
}

func (r *Repo) gitPath(ctx context.Context, name string) (string, error) {
	path, err := r.Git(ctx, "rev-parse", "--path-format=absolute", "--git-path", name)
	return strings.TrimSpace(path), err
}

// CleanCommitMessage applies Git's configured cleanup for a noninteractive
// message file. The result can then be approved and stored byte for byte.
func (r *Repo) CleanCommitMessage(ctx context.Context, message string) (string, error) {
	mode, err := r.Git(ctx, "config", "--default=default", "--get", "commit.cleanup")
	if err != nil {
		return "", err
	}
	args := []string{"stripspace"}
	switch strings.TrimSpace(mode) {
	case "verbatim":
		return message, nil
	case "strip":
		args = append(args, "--strip-comments")
	case "default", "whitespace", "scissors":
		// Git only cuts the scissors line when an editor is opened.
	default:
		return "", fmt.Errorf("unknown git commit cleanup mode %q", strings.TrimSpace(mode))
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = r.Dir
	cmd.Stdin = strings.NewReader(message)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("clean commit message: %w", err)
	}
	return string(out), nil
}

// PreviewCommit uses private indexes and an ephemeral Git object database.
// Filters and attributes are evaluated the same way they are for git add.
func (r *Repo) PreviewCommit(ctx context.Context, paths []string) (*CommitTree, error) {
	dir, err := os.MkdirTemp("", "devflow-commit-preview-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	objects, err := r.gitPath(ctx, "objects")
	if err != nil {
		return nil, err
	}
	private := filepath.Join(dir, "objects")
	if err := os.MkdirAll(filepath.Join(private, "info"), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(private, "info", "alternates"), []byte(objects+"\n"), 0600); err != nil {
		return nil, err
	}
	c := &commitIndex{repo: r, dir: dir, env: append(os.Environ(), "GIT_OBJECT_DIRECTORY="+private)}
	return c.build(ctx, paths)
}

func (c *commitIndex) build(ctx context.Context, paths []string) (*CommitTree, error) {
	r := c.repo
	// This transaction guard must run before Git moves the ref. Git
	// can disable the entire reference-transaction event independently of the
	// hook directory. Respect that configuration by refusing the operation.
	enabled, err := r.Git(ctx, "config", "--type=bool", "--default=true", "--get", "hook.reference-transaction.enabled")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(enabled) != "true" {
		return nil, errors.New("exact commits require the Git reference-transaction hook event to be enabled")
	}
	index, err := r.gitPath(ctx, "index")
	if err != nil {
		return nil, err
	}
	c.full = filepath.Join(c.dir, "full.index")
	b, err := os.ReadFile(index)
	if err == nil {
		if err = os.WriteFile(c.full, b, 0600); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ref, err := r.Git(ctx, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		ref = "HEAD"
	} else {
		ref = strings.TrimSpace(ref)
	}
	parent, err := r.Git(ctx, "rev-parse", "--verify", "HEAD^{commit}")
	base := strings.TrimSpace(parent)
	if err != nil {
		if ref == "HEAD" {
			return nil, err
		}
		// An unborn branch is the only valid HEAD without a parent commit.
		if exists, _ := r.Git(ctx, "show-ref", "--verify", ref); exists != "" {
			return nil, err
		}
		base, err = c.git(ctx, c.full, "hash-object", "-w", "-t", "tree", "--stdin")
		if err != nil {
			return nil, err
		}
		base = strings.TrimSpace(base)
	}
	parent = strings.TrimSpace(parent)
	gitDir, err := r.Git(ctx, "rev-parse", "--path-format=absolute", "--git-dir")
	if err != nil {
		return nil, err
	}
	s := &CommitTree{Repository: r.Dir, GitDir: strings.TrimSpace(gitDir), Ref: ref, Parent: parent, Parents: parent}
	mergeHead, err := r.gitPath(ctx, "MERGE_HEAD")
	if err != nil {
		return nil, err
	}
	if b, readErr := os.ReadFile(mergeHead); readErr == nil {
		if len(paths) > 0 {
			return nil, errors.New("cannot commit selected paths during a merge")
		}
		s.Parents = strings.TrimSpace(parent + " " + strings.Join(strings.Fields(string(b)), " "))
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}
	c.selected = c.full
	if len(paths) > 0 {
		if _, err = c.git(ctx, c.full, append([]string{"add", "--"}, paths...)...); err != nil {
			return nil, err
		}
		updated, err := c.git(ctx, c.full, "write-tree")
		if err != nil {
			return nil, err
		}
		c.selected = filepath.Join(c.dir, "selected.index")
		if _, err = c.git(ctx, c.selected, "read-tree", base); err != nil {
			return nil, err
		}
		args := append([]string{"restore", "--source=" + strings.TrimSpace(updated), "--staged", "--"}, paths...)
		if _, err = c.git(ctx, c.selected, args...); err != nil {
			return nil, err
		}
	}
	tree, err := c.git(ctx, c.selected, "write-tree")
	if err != nil {
		return nil, err
	}
	s.Tree = strings.TrimSpace(tree)
	names, err := c.git(ctx, c.selected, "diff", "--name-only", "--no-renames", "-z", base, s.Tree, "--")
	if err != nil {
		return nil, err
	}
	s.Files = strings.FieldsFunc(names, func(r rune) bool { return r == 0 })
	if len(s.Files) == 0 {
		return nil, errors.New("nothing staged to commit")
	}
	s.Diff, err = c.git(ctx, c.selected, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--unified=0", base, s.Tree, "--")
	return s, err
}

// CommitExact holds the normal index lock, then reconstructs the selected tree.
// Other staged entries are retained when only selected paths are committed.
func (r *Repo) CommitExact(ctx context.Context, expected *CommitTree, message string, paths []string) (string, error) {
	index, err := r.gitPath(ctx, "index")
	if err != nil {
		return "", err
	}
	lock := index + ".lock"
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("lock Git index: %w", err)
	}
	defer os.Remove(lock)
	if err = f.Close(); err != nil {
		return "", err
	}
	// Keep temporary indexes beside the original so replacing it is atomic.
	dir, err := os.MkdirTemp(filepath.Dir(index), "devflow-commit-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	c := &commitIndex{repo: r, dir: dir, env: os.Environ()}
	actual, err := c.build(ctx, paths)
	if err != nil {
		return "", err
	}
	if actual.Ref != expected.Ref || actual.Parents != expected.Parents || actual.Tree != expected.Tree || actual.Repository != expected.Repository || actual.GitDir != expected.GitDir {
		return "", errors.New("commit changed since preparation; prepare the current changes again")
	}
	hooks, err := r.gitPath(ctx, "hooks")
	if err != nil {
		return "", err
	}
	guard, err := c.guardHooks(hooks, runtime.GOOS == "windows")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "message"), []byte(message), 0600); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "git", "-c", "core.hooksPath="+guard, "-c", "hook.reference-transaction.enabled=true", "commit", "--cleanup=verbatim", "--file=-")
	cmd.Dir = r.Dir
	cmd.Env = slices.Concat(c.env, []string{
		"GIT_INDEX_FILE=" + c.selected, "DEVFLOW_ORIGINAL_HOOKS=" + hooks,
		"DEVFLOW_EXPECTED_REF=" + actual.Ref, "DEVFLOW_EXPECTED_TREE=" + actual.Tree,
		"DEVFLOW_EXPECTED_PARENTS=" + actual.Parents, "DEVFLOW_COMMIT_TMP=" + dir,
	})
	cmd.Stdin = strings.NewReader(message)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	// A successful ref update is the commit point. The lock prevents any normal
	// staging operation from racing with installation of the full index.
	if err = os.Rename(c.full, index); err != nil {
		return "", fmt.Errorf("commit created but could not install updated index: %w", err)
	}
	id, err := os.ReadFile(filepath.Join(dir, "committed"))
	if err != nil {
		return "", fmt.Errorf("read committed revision: %w", err)
	}
	sha, err := r.Git(ctx, "rev-parse", "--short", strings.TrimSpace(string(id)))
	return strings.TrimSpace(sha), err
}

// Git invokes this hook after it has locked the ref, before moving it. This
// catches branch switches and hooks that amend the tree during git commit.
const transactionGuard = `#!/bin/sh
set -eu
log="$DEVFLOW_COMMIT_TMP/ref-input.$$"
trap 'rm -f "$log"' EXIT
cat >"$log"
if [ "$1" = prepared ]; then
	while read -r old new ref; do
		case "$ref" in HEAD|refs/heads/*) ;; *) continue ;; esac
		case "$ref" in HEAD) actual=$(git symbolic-ref -q HEAD || echo HEAD) ;; *) actual=$ref ;; esac
		if [ "$actual" != "$DEVFLOW_EXPECTED_REF" ] ||
		   [ "$(git --no-replace-objects show -s --no-show-signature --no-notes --format=%T "$new")" != "$DEVFLOW_EXPECTED_TREE" ] ||
		   [ "$(git --no-replace-objects show -s --no-show-signature --no-notes --format=%P "$new")" != "$DEVFLOW_EXPECTED_PARENTS" ]; then
			echo "commit ref, parents or tree changed after approval" >&2
			exit 1
		fi
		git --no-replace-objects cat-file commit "$new" | {
			while IFS= read -r header && [ -n "$header" ]; do :; done
			cat
		} > "$DEVFLOW_COMMIT_TMP/actual-message"
		if ! cmp -s "$DEVFLOW_COMMIT_TMP/message" "$DEVFLOW_COMMIT_TMP/actual-message"; then
			echo "commit message changed after approval" >&2
			exit 1
		fi
		printf '%s\n' "$new" > "$DEVFLOW_COMMIT_TMP/committed"
	done <"$log"
fi
`

func (c *commitIndex) guardHooks(original string, windows bool) (string, error) {
	dir := filepath.Join(c.dir, "hooks")
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(original)
	// Git supports disabling traditional hooks by pointing core.hooksPath
	// at /dev/null. A missing or non-directory path has no hooks to forward.
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		if info, statErr := os.Stat(original); statErr != nil || info.IsDir() {
			return "", err
		}
	}
	hooks := make(map[string]string)
	for _, entry := range entries {
		name := entry.Name()
		info, err := os.Stat(filepath.Join(original, name))
		// Git ignores dangling links.
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !info.Mode().IsRegular() || !windows && info.Mode().Perm()&0111 == 0 {
			continue
		}
		suffix := ""
		if windows {
			// Windows has no executable mode bit. Git looks up the bare
			// name first, then the same name with an .exe suffix.
			name = strings.ToLower(name)
			if strings.HasSuffix(name, ".exe") {
				name = strings.TrimSuffix(name, ".exe")
				if _, bare := hooks[name]; bare {
					continue
				}
				suffix = ".exe"
			}
		}
		hooks[name] = suffix
	}
	guard := transactionGuard
	for name, suffix := range hooks {
		target := `"$DEVFLOW_ORIGINAL_HOOKS/${0##*/}` + suffix + `"`
		if name == "reference-transaction" {
			guard += target + ` "$@" <"$log"` + "\n"
			continue
		}
		forward := "#!/bin/sh\nexec " + target + ` "$@"` + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(forward), 0700); err != nil {
			return "", err
		}
	}
	return dir, os.WriteFile(filepath.Join(dir, "reference-transaction"), []byte(guard), 0700)
}
