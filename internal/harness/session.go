package harness

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ryanhelms/agentflow/internal/harness/acpregistry"
)

// Phase 7 (lane 7.2): what an ACP session needs from detection. This file
// adds to the package; no other harness file changes.

// ErrNotReady: the harness is not ready for a session. The error text is
// agentflow's own; Locate also returns the Harness, whose Probe, State and
// Fix say why and what to do.
var ErrNotReady = errors.New("harness: not ready for a session")

// ErrNotFound: no enabled definition has this ID.
var ErrNotFound = errors.New("harness: no such harness")

// Locate resolves one definition with the same layers, PATH rules and
// probe cache as Detect, and never probes. home is the agentflow home
// ("" means config.HomeDir()); version is unused today and kept for the
// probe cache's identity rules; workDirs are the directories whose
// repositories' worktrees are skipped on PATH.
//
// It returns ErrNotReady unless the state is native or adapter_installed
// with a probe result ok (fresh or cached) for the current identity, and
// ErrNotFound for an unknown or disabled ID. A found harness of a plugin
// that chose the launch fields carries NotePluginLaunch.
func Locate(ctx context.Context, home, version string, workDirs func() []string, id string) (Found, Harness, error) {
	_ = version
	d := New(Options{Home: home, WorkDirs: workDirs, Prober: NoProber{}}).(*detector)
	st, _ := d.settings()
	if st.homeOK && st.home != "" {
		d.o.Store = NewFileStore(st.home)
	}
	pathValue := d.o.Getenv("PATH")
	for _, dir := range filepath.SplitList(pathValue) {
		if filepath.IsAbs(dir) {
			if err := testGuard(dir); err != nil {
				return Found{}, Harness{}, err
			}
		}
	}
	defs, err := d.layers(st)
	if err != nil {
		return Found{}, Harness{}, err
	}
	var dirs []string
	if workDirs != nil {
		dirs = workDirs()
	}
	repos, known := RepositoryWorktrees(ctx, dirs...)
	fd := newFinder(pathValue, repos)
	fd.reposUnknown = !known
	res, err := fd.run(defs)
	if err != nil {
		return Found{}, Harness{}, fmt.Errorf("harness: locate: %w", err)
	}
	defs = dropSameProgram(defs, res)
	for _, def := range defs {
		if def.ID != id {
			continue
		}
		f, ok := res.found[id]
		h, err := d.harness(ctx, st, def, f, ok, DetectOptions{IDs: []string{id}, Requester: RequesterPerson}, false, d.o.Now())
		if err != nil {
			return Found{}, Harness{}, err
		}
		if !ok || f.Path == "" || f.shim || !Ready(h) {
			return f.Found, h, ErrNotReady
		}
		return f.Found, h, nil
	}
	return Found{}, Harness{}, ErrNotFound
}

// layers composes the definitions the way Detect does.
func (d *detector) layers(st settings) ([]Layered, error) {
	cat, err := acpregistry.Load()
	if err != nil {
		return nil, err
	}
	overlay, err := LoadOverlay()
	if err != nil {
		return nil, err
	}
	layers := Layers{Catalog: cat, Overlay: overlay, Platform: d.o.Platform}
	if st.homeOK && st.user != nil {
		layers.Personal = st.user.AI.Harnesses.Overrides
		layers.Disabled = st.user.AI.Harnesses.Disabled
		plugins, _ := pluginHarnesses(st.home, st.user)
		layers.Plugins = plugins
	}
	defs, _ := Compose(layers)
	return defs, nil
}

// Ready reports whether h may start a session: native or
// adapter_installed, with an ok probe (fresh or cached).
func Ready(h Harness) bool {
	if h.State != StateNative && h.State != StateAdapterInstalled {
		return false
	}
	switch h.Probe.Status {
	case ProbeOK:
		return true
	case ProbeCached:
		return h.Probe.Code == CodeOK
	}
	return false
}

// GuardPath is the test guard ("Phase 6 test guard"): in a test binary, a
// path outside os.TempDir() fails with ErrTestGuard. Outside a test binary
// it allows everything. The session host calls it on the harness file
// right before jsonrpc.Launch, and on every terminal program.
func GuardPath(path string) error { return testGuard(path) }

// SessionPATH returns the entries of pathValue that pass the phase 6 PATH
// checks (relative, missing, inside_repository against every worktree of
// the repositories containing workDirs, node_modules, unsafe_permissions),
// cleaned and without duplicates, in order. When the repositories cannot be
// told, every entry counts as inside one (fail closed). In a test binary an
// entry outside the temporary directory fails with ErrTestGuard.
func SessionPATH(ctx context.Context, pathValue string, workDirs []string) ([]string, []SkippedDir, error) {
	repos, known := RepositoryWorktrees(ctx, workDirs...)
	fd := newFinder(pathValue, repos)
	fd.reposUnknown = !known
	var skipped []SkippedDir
	entries, err := fd.entries(func(dir, reason string) { skipped = append(skipped, SkippedDir{Dir: dir, Reason: reason}) })
	if err != nil {
		return nil, skipped, err
	}
	out := make([]string, 0, len(entries))
	seen := map[string]bool{}
	for _, e := range entries {
		if !seen[e.dir] {
			seen[e.dir] = true
			out = append(out, e.dir)
		}
	}
	return out, skipped, nil
}

// SessionProgramOK reports whether p, an absolute path, passes the phase 6
// file checks (a regular, executable file whose resolved target is not
// writable by others) and neither p nor its target is inside a worktree of
// the repositories containing workDirs. The session uses it for SHELL.
func SessionProgramOK(ctx context.Context, p string, workDirs []string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	repos, known := RepositoryWorktrees(ctx, workDirs...)
	fd := newFinder("", repos)
	fd.reposUnknown = !known
	if fd.insideRepository(resolved(filepath.Dir(p))) {
		return false
	}
	hit, err := fd.absolute(p, func(string, string) {})
	if err != nil || hit == nil || hit.shim {
		return false
	}
	return !fd.insideRepository(hit.Target)
}

// HomeUnsafe reports whether the agentflow home fails the phase 6 home
// checks: not owned by this user, or writable by others.
func HomeUnsafe(dir string) bool { return homeUnsafe(dir) }
