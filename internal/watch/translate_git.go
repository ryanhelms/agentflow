package watch

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ryanhelms/agentflow/internal/bus"
)

// GitTranslator translates git directory changes into git.* events. It
// remembers, per worktree, HEAD's ref and OID, the index's stat data and
// the current operation, and per repository the loose refs, packed-refs
// and the worktrees/ entries, so it fills previous_* fields and skips
// changes that change nothing.
type GitTranslator struct {
	mu        sync.Mutex
	worktrees map[Key]*worktreeGit
	repos     map[bus.RepoID]*repoGit
}

type worktreeGit struct {
	t     Target
	ref   string
	oid   string
	op    bus.Operation
	index stamp
}

type repoGit struct {
	common    string
	targets   map[bus.WorktreeID]bool
	packed    stamp
	loose     map[string][sha256.Size]byte
	complete  bool // loose holds every ref, so an unknown removed path was no ref
	worktrees map[string]bool
}

var (
	_ Translator = (*GitTranslator)(nil)
	_ Tracker    = (*GitTranslator)(nil)
)

// NewGitTranslator returns an empty GitTranslator.
func NewGitTranslator() *GitTranslator {
	return &GitTranslator{worktrees: map[Key]*worktreeGit{}, repos: map[bus.RepoID]*repoGit{}}
}

// Track implements Tracker: it records the worktree's HEAD, index and
// operation, and the first time it sees a repository its refs and
// worktrees.
func (g *GitTranslator) Track(ctx context.Context, t Target) error {
	ref, oid, err := resolveHead(ctx, t.GitDir)
	g.mu.Lock()
	defer g.mu.Unlock()
	g.worktrees[KeyOf(t)] = &worktreeGit{t: t, ref: ref, oid: oid, op: operation(t.GitDir),
		index: statStamp(filepath.Join(t.GitDir, "index"))}
	r, ok := g.repos[t.Repo]
	if !ok {
		r = &repoGit{common: t.CommonDir, targets: map[bus.WorktreeID]bool{},
			packed: statStamp(filepath.Join(t.CommonDir, "packed-refs")), worktrees: listWorktrees(t.CommonDir)}
		r.loose, r.complete = scanLooseRefs(t.CommonDir)
		g.repos[t.Repo] = r
	}
	r.targets[t.Worktree] = true
	return err
}

// Untrack implements Tracker.
func (g *GitTranslator) Untrack(t Target) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.worktrees, KeyOf(t))
	if r, ok := g.repos[t.Repo]; ok {
		delete(r.targets, t.Worktree)
		if len(r.targets) == 0 {
			delete(g.repos, t.Repo)
		}
	}
}

// Translate implements Translator. A Batch with an empty Worktree carries
// repository-wide paths (refs/, packed-refs, worktrees/); any other Batch
// carries one worktree's git dir.
func (g *GitTranslator) Translate(ctx context.Context, b Batch) ([]bus.Event, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if b.Target.Worktree == "" {
		return g.translateRepo(ctx, b)
	}
	return g.translateWorktree(ctx, b)
}

func (g *GitTranslator) translateWorktree(ctx context.Context, b Batch) ([]bus.Event, error) {
	w, ok := g.worktrees[KeyOf(b.Target)]
	if !ok {
		return nil, nil
	}
	var head, index, op bool
	for _, c := range b.Changes {
		typ, _ := Classify(w.t, c.Path)
		switch typ {
		case bus.TypeGitHeadChanged:
			head = true
		case bus.TypeGitIndexChanged:
			index = true
		case bus.TypeGitOperationChanged:
			op = true
		}
	}
	at := batchTime(b)
	var out []bus.Event
	var errs []error
	add := func(e bus.Event, err error) {
		if err != nil {
			errs = append(errs, err)
			return
		}
		out = append(out, e)
	}
	if op {
		if now := operation(w.t.GitDir); now != w.op {
			add(newEvent(bus.TypeGitOperationChanged, SourceGit, w.t.Repo, w.t.Worktree, at,
				bus.GitOperationChanged{Operation: now, Previous: w.op}))
			w.op = now
		}
	}
	if head {
		e, changed, err := g.headEvent(ctx, w, at)
		if err != nil {
			errs = append(errs, err)
		} else if changed {
			out = append(out, e)
		}
	}
	if index {
		if now := statStamp(filepath.Join(w.t.GitDir, "index")); now != w.index {
			w.index = now
			add(newEvent(bus.TypeGitIndexChanged, SourceGit, w.t.Repo, w.t.Worktree, at, bus.GitIndexChanged{}))
		}
	}
	return out, errors.Join(errs...)
}

// headEvent resolves HEAD again and returns git.head.changed when its ref
// or OID moved.
func (g *GitTranslator) headEvent(ctx context.Context, w *worktreeGit, at time.Time) (bus.Event, bool, error) {
	ref, oid, err := resolveHead(ctx, w.t.GitDir)
	if err != nil {
		return bus.Event{}, false, err
	}
	if ref == w.ref && oid == w.oid {
		return bus.Event{}, false, nil
	}
	e, err := newEvent(bus.TypeGitHeadChanged, SourceGit, w.t.Repo, w.t.Worktree, at,
		bus.GitHeadChanged{Ref: ref, OID: oid, PreviousRef: w.ref, PreviousOID: w.oid})
	if err != nil {
		return bus.Event{}, false, err
	}
	w.ref, w.oid = ref, oid
	return e, true, nil
}

func (g *GitTranslator) translateRepo(ctx context.Context, b Batch) ([]bus.Event, error) {
	r, ok := g.repos[b.Target.Repo]
	if !ok {
		return nil, nil
	}
	var rescan, worktrees bool
	changed := map[string]bool{}
	for _, c := range b.Changes {
		if Excluded(r.common, c.Path) {
			continue
		}
		rel, ok := under(r.common, c.Path)
		if !ok {
			continue
		}
		switch {
		case rel == "packed-refs":
			if now := statStamp(c.Path); now != r.packed {
				r.packed = now
				rescan = true
			}
		case rel == "refs":
			rescan = true // a synthesized rescan: refs may have changed anywhere
		case strings.HasPrefix(rel, "refs/"):
			if r.looseChanged(rel, c.Path) {
				changed[rel] = true
			}
		case rel == "worktrees" || strings.HasPrefix(rel, "worktrees/"):
			worktrees = true
		}
	}
	at := batchTime(b)
	var out []bus.Event
	var errs []error
	if rescan || len(changed) > 0 {
		refs := make([]string, 0, len(changed))
		if !rescan {
			for ref := range changed {
				refs = append(refs, ref)
			}
			sort.Strings(refs)
		}
		e, err := newEvent(bus.TypeGitRefsChanged, SourceGit, b.Target.Repo, "", at, bus.GitRefsChanged{Refs: refs})
		if err != nil {
			errs = append(errs, err)
		} else {
			out = append(out, e)
			// A commit on the checked-out branch writes only the branch
			// ref, so every worktree's HEAD is resolved again.
			wts := make([]bus.WorktreeID, 0, len(r.targets))
			for wt := range r.targets {
				wts = append(wts, wt)
			}
			sort.Slice(wts, func(i, j int) bool { return wts[i] < wts[j] })
			for _, wt := range wts {
				w, ok := g.worktrees[Key{Repo: b.Target.Repo, Worktree: wt}]
				if !ok {
					continue
				}
				h, moved, err := g.headEvent(ctx, w, at)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				if moved {
					h.CausationID, h.CorrelationID = e.ID, e.ID
					out = append(out, h)
				}
			}
		}
	}
	if worktrees {
		if now := listWorktrees(r.common); !sameSet(now, r.worktrees) {
			r.worktrees = now
			e, err := newEvent(bus.TypeGitWorktreesChanged, SourceGit, b.Target.Repo, "", at, bus.GitWorktreesChanged{})
			if err != nil {
				errs = append(errs, err)
			} else {
				out = append(out, e)
			}
		}
	}
	return out, errors.Join(errs...)
}

// looseChanged updates the remembered content of one loose ref and
// reports whether it changed. Directories are not refs.
func (r *repoGit) looseChanged(rel, path string) bool {
	fi, err := os.Lstat(path)
	switch {
	case err == nil && fi.IsDir():
		return false
	case err == nil:
		b, err := os.ReadFile(path)
		if err != nil {
			return true
		}
		sum := sha256.Sum256(b)
		old, known := r.loose[rel]
		r.loose[rel] = sum
		return !known || old != sum
	default:
		if _, known := r.loose[rel]; known {
			delete(r.loose, rel)
			return true
		}
		return !r.complete
	}
}

// scanLooseRefs reads every loose ref under refs/, up to
// PollRefsMaxEntries. complete is false when the walk stopped early.
func scanLooseRefs(common string) (refs map[string][sha256.Size]byte, complete bool) {
	refs = map[string][sha256.Size]byte{}
	dir, err := os.OpenRoot(common)
	if err != nil {
		return refs, false
	}
	defer func() { _ = dir.Close() }()
	n := 0
	stop := errors.New("stop")
	err = fs.WalkDir(dir.FS(), "refs", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // a ref removed during the walk is simply skipped
		}
		if n++; n > PollRefsMaxEntries {
			return stop
		}
		if d.IsDir() || strings.HasSuffix(rel, ".lock") {
			return nil
		}
		if b, err := dir.ReadFile(rel); err == nil {
			refs[rel] = sha256.Sum256(b)
		}
		return nil
	})
	return refs, err == nil
}

// listWorktrees returns the entries of <common>/worktrees.
func listWorktrees(common string) map[string]bool {
	out := map[string]bool{}
	list, _ := os.ReadDir(filepath.Join(common, "worktrees"))
	for _, e := range list {
		if e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// operation returns the multi-step operation in progress in gitDir.
func operation(gitDir string) bus.Operation {
	exists := func(name string) bool {
		_, err := os.Lstat(filepath.Join(gitDir, name))
		return err == nil
	}
	switch {
	case exists("rebase-merge") || exists("rebase-apply"):
		return bus.OperationRebase
	case exists("MERGE_HEAD"):
		return bus.OperationMerge
	case exists("CHERRY_PICK_HEAD"):
		return bus.OperationCherryPick
	case exists("REVERT_HEAD"):
		return bus.OperationRevert
	case exists("BISECT_LOG"):
		return bus.OperationBisect
	}
	return bus.OperationNone
}

// resolveHead returns the ref HEAD points to (empty when detached) and the
// commit it resolves to (empty on an unborn branch). It asks git, so
// reftable repositories and per-worktree refs resolve correctly; neither
// command writes to the repository.
func resolveHead(ctx context.Context, gitDir string) (ref, oid string, err error) {
	out, err := runGit(ctx, gitDir, "symbolic-ref", "-q", "HEAD")
	switch {
	case err == nil:
		ref = out
	case exitCode(err) == 1: // detached
	default:
		return "", "", fmt.Errorf("resolve HEAD in %s: %w", gitDir, err)
	}
	out, err = runGit(ctx, gitDir, "rev-parse", "-q", "--verify", "HEAD")
	switch {
	case err == nil:
		oid = out
	case exitCode(err) == 1: // unborn branch
	default:
		return "", "", fmt.Errorf("resolve HEAD in %s: %w", gitDir, err)
	}
	return ref, oid, nil
}

func runGit(ctx context.Context, gitDir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir=" + gitDir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
