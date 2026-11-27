package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ryanhelms/agentflow/internal/config"
	"github.com/ryanhelms/agentflow/internal/gitx"
	"github.com/ryanhelms/agentflow/internal/harness/harnessdef"
	"github.com/ryanhelms/agentflow/internal/worktree"
)

// The finder ("Phase 6 finding a harness on PATH"): it looks up each
// definition's command, then its base commands, in the PATH of the process
// that runs detection. Every PATH entry and every found file passes the
// checks first; a skipped entry is reported once with its reason.

type finder struct {
	path    string   // the PATH value
	repos   []string // resolved worktree roots of the repositories agentflow works in
	// reposUnknown: the repositories could not be told, so every PATH
	// entry counts as inside one (fail closed).
	reposUnknown bool
	windows bool
	guard   func(string) error
}

type pathEntry struct {
	dir  string // as on PATH
	real string // links resolved
}

type found struct {
	Found
	shim bool // a Windows .cmd or .bat file: found, never started
}

type finderResult struct {
	found   map[string]found // by definition ID
	skipped []SkippedDir
}

func newFinder(pathValue string, repos []string) *finder {
	return &finder{path: pathValue, repos: repos, windows: runtime.GOOS == "windows", guard: testGuard}
}

func (f *finder) run(defs []Layered) (finderResult, error) {
	res := finderResult{found: map[string]found{}}
	seen := map[string]bool{}
	skip := func(dir, reason string) {
		if key := reason + "\x00" + dir; !seen[key] {
			seen[key] = true
			res.skipped = append(res.skipped, SkippedDir{Dir: dir, Reason: reason})
		}
	}
	entries, err := f.entries(skip)
	if err != nil {
		return res, err
	}
	for _, d := range defs {
		def := d.Definition
		var hit *found
		if def.Command != "" {
			if filepath.IsAbs(def.Command) {
				hit, err = f.absolute(def.Command, skip)
			} else {
				hit, err = f.lookup(entries, def.Command, skip)
			}
			if err != nil {
				return res, err
			}
		}
		var base *Found
		for _, c := range def.BaseCommands {
			b, err := f.lookup(entries, c, skip)
			if err != nil {
				return res, err
			}
			if b == nil || b.shim {
				continue
			}
			id, err := IdentityOf(b.Path, def.StatusArgs, nil)
			if err != nil {
				continue
			}
			bf := b.Found
			bf.ID, bf.Identity = d.ID, id
			bf.Definition = harnessdef.Definition{Name: def.BaseName, Command: c}
			base = &bf
			break
		}
		if hit == nil {
			if base != nil {
				res.found[d.ID] = found{Found: Found{ID: d.ID, Definition: def, Sources: d.Sources, BaseFound: base}}
			}
			continue
		}
		hit.ID, hit.Definition, hit.Sources, hit.BaseFound = d.ID, def, d.Sources, base
		hit.Identity, err = IdentityOf(hit.Path, def.Args, versionArgs(def))
		if err != nil {
			continue // the file went away between the lookup and now
		}
		res.found[d.ID] = *hit
	}
	return res, nil
}

// entries checks every PATH entry once, in order.
func (f *finder) entries(skip func(dir, reason string)) ([]pathEntry, error) {
	var out []pathEntry
	dup := map[string]bool{}
	for _, dir := range filepath.SplitList(f.path) {
		if dup[dir] {
			continue
		}
		dup[dir] = true
		if dir == "" || dir == "." || !filepath.IsAbs(dir) {
			skip(dir, SkipRelative)
			continue
		}
		if err := f.guard(dir); err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			continue // missing: not reported
		}
		if info, err := os.Stat(real); err != nil || !info.IsDir() {
			continue
		}
		switch {
		case f.insideRepository(real):
			skip(dir, SkipInsideRepository)
		case hasSegment(dir, "node_modules") || hasSegment(real, "node_modules"):
			skip(dir, SkipNodeModules)
		case dirUnsafe(real):
			skip(dir, SkipUnsafePermissions)
		default:
			out = append(out, pathEntry{dir: filepath.Clean(dir), real: real})
		}
	}
	return out, nil
}

// lookup finds name in the checked entries.
func (f *finder) lookup(entries []pathEntry, name string, skip func(dir, reason string)) (*found, error) {
	for _, e := range entries {
		for _, cand := range f.candidates(name) {
			// The entry's links are resolved, the file's own are not, so
			// the path stays inside the launch root (LaunchRoot).
			p := filepath.Join(e.real, cand)
			info, err := os.Stat(p)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if !f.windows && info.Mode().Perm()&0o111 == 0 {
				continue
			}
			hit, ok, err := f.check(p, e.dir, skip)
			if err != nil || ok {
				return hit, err
			}
		}
	}
	return nil, nil
}

// absolute checks a personal absolute command: no PATH walk and no
// repository or node_modules check (the person named that file), but the
// guard and the permission checks.
func (f *finder) absolute(p string, skip func(dir, reason string)) (*found, error) {
	if err := f.guard(p); err != nil {
		return nil, err
	}
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() || (!f.windows && info.Mode().Perm()&0o111 == 0) {
		return nil, nil
	}
	dir := filepath.Dir(filepath.Clean(p))
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	hit, _, err := f.check(filepath.Join(dir, filepath.Base(p)), dir, skip)
	return hit, err
}

// check resolves a candidate file and applies the file checks. ok is false
// when the file is skipped and the lookup should go on.
func (f *finder) check(p, dir string, skip func(dir, reason string)) (*found, bool, error) {
	if err := f.guard(p); err != nil {
		return nil, false, err
	}
	target, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil, false, nil
	}
	if err := f.guard(target); err != nil {
		return nil, false, err
	}
	if fileUnsafe(target) {
		skip(p, SkipUnsafePermissions)
		return nil, false, nil
	}
	hit := &found{Found: Found{Path: p, Target: target, PathDir: dir}}
	if f.windows {
		switch strings.ToLower(filepath.Ext(p)) {
		case ".cmd", ".bat":
			hit.shim = true
			skip(p, SkipShim)
		}
	}
	return hit, true, nil
}

// candidates are the file names tried for a command: the name itself on
// Unix; on Windows the .exe first, then the shims, which are found but
// never started.
func (f *finder) candidates(name string) []string {
	if !f.windows {
		return []string{name}
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".exe", ".cmd", ".bat":
		return []string{name}
	}
	return []string{name + ".exe", name + ".cmd", name + ".bat"}
}

func (f *finder) insideRepository(real string) bool {
	if f.reposUnknown {
		return true
	}
	for _, r := range f.repos {
		if inside(real, r) {
			return true
		}
	}
	return false
}

func hasSegment(p, seg string) bool {
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if strings.EqualFold(part, seg) {
			return true
		}
	}
	return false
}

// RepositoryWorktrees returns the resolved roots of every worktree of the
// repositories that contain dirs, and of their superprojects when a dir is
// inside a submodule. A directory that is in no repository adds nothing.
// The finder skips PATH entries inside these roots (inside_repository).
//
// It fails closed: when git cannot list a repository's worktrees, the
// repository's own root (found by walking up for .git) is used; when even
// that cannot be told, known is false and the finder skips every PATH
// entry as inside_repository.
func RepositoryWorktrees(ctx context.Context, dirs ...string) (roots []string, known bool) {
	known = true
	seen := map[string]bool{}
	add := func(p string) {
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = filepath.Clean(p)
		}
		if !seen[real] {
			seen[real] = true
			roots = append(roots, real)
		}
	}
	visited := map[string]bool{}
	var visit func(d string, depth int)
	visit = func(d string, depth int) {
		if d == "" || visited[d] || depth > 8 {
			return
		}
		visited[d] = true
		repo, inside, ok := config.EnclosingRepository(d)
		switch {
		case !ok:
			known = false
			return
		case !inside:
			return
		}
		add(repo)
		if list, err := worktree.List(ctx, d); err == nil {
			for _, e := range list {
				add(e.Path)
			}
		}
		// A submodule's superproject is a repository agentflow works in too.
		if out, err := (&gitx.Repo{Dir: d}).Git(ctx, "rev-parse", "--show-superproject-working-tree"); err == nil {
			if super := strings.TrimSpace(out); super != "" {
				visit(super, depth+1)
			}
		}
	}
	for _, d := range dirs {
		visit(d, 0)
	}
	return roots, known
}

// identityFields are hashed into a harness's identity.
type identityFields struct {
	Path        string   `json:"path"`
	Target      string   `json:"target"`
	Size        int64    `json:"size"`
	ModTime     int64    `json:"mtime_ns"`
	Device      uint64   `json:"dev,omitempty"`
	Inode       uint64   `json:"ino,omitempty"`
	Args        []string `json:"args"`
	VersionArgs []string `json:"version_args"`
}

// IdentityOf is the identity of a found file: "sha256:<hex>" of its PATH
// path, resolved target, size, modification time, device and inode (Unix),
// and the effective args and version args. Any change is a new identity.
// Lane 6.2 uses it for a base CLI's identity (ProbeRecord.BaseIdentity) with
// the status args.
func IdentityOf(path string, args, versionArgs []string) (string, error) {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	dev, ino := fileID(info)
	b, err := json.Marshal(identityFields{
		Path: path, Target: target, Size: info.Size(), ModTime: info.ModTime().UnixNano(),
		Device: dev, Inode: ino, Args: nonNil(args), VersionArgs: nonNil(versionArgs),
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// LaunchRoot is the Root a found file is started with: the deepest directory
// that contains both the PATH entry and the resolved target
// (/opt/homebrew/bin/claude-agent-acp -> /opt/homebrew/lib/... gives
// /opt/homebrew).
func LaunchRoot(pathDir, target string) string {
	a := filepath.Clean(pathDir)
	if real, err := filepath.EvalSymlinks(a); err == nil {
		a = real
	}
	b := filepath.Dir(filepath.Clean(target))
	for !inside(b, a) {
		parent := filepath.Dir(a)
		if parent == a {
			break
		}
		a = parent
	}
	return a
}
