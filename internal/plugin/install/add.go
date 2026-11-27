package install

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/ryanhelms/agentflow/internal/plugin/manifest"
)

// Result is what Add did.
type Result struct {
	Outcome Outcome
	// Entry is the registry entry of the plugin now.
	Entry manifest.Installed
	// Previous is the entry that was replaced, for an upgrade.
	Previous *manifest.Installed
}

// errNothingToDo ends the registry update of an Unchanged add without a
// write.
var errNothingToDo = errors.New("nothing to do")

// Add installs the plan under the registry lock: the files go to
// <home>/plugins/<id>/<hash prefix>/ with read-only modes (directories 0500,
// files 0400, executables 0500), the copy is hashed again, and the registry
// entry is written with the source path, the time and the manifest. The
// plugin's data directory is created, 0700, and survives upgrades. The
// plugin is not enabled: nothing in the personal layer changes. An upgrade
// removes the previous version's copy after the registry write.
//
// Add re-reads the registry under its lock, so an add that races another
// add or a remove never loses an entry. An exact version that is installed
// and intact is left as it is (Unchanged).
func Add(home string, plan *AddPlan, now time.Time) (Result, error) {
	m := plan.Manifest
	dir := manifest.InstallPath(home, m.ID, plan.Hash)
	data := manifest.DataPath(home, m.ID)
	if dir == "" || data == "" {
		return Result{}, errors.New("plugin add: the plugin's ID or hash is not valid")
	}
	entry := manifest.Installed{ID: m.ID, Version: m.Version, Tier: m.Tier, Hash: plan.Hash, Dir: dir, Source: plan.Source,
		InstalledAt: now.UTC().Truncate(time.Second), Manifest: m}
	res := Result{Outcome: Installed, Entry: entry}
	wrote := false
	err := manifest.UpdateRegistry(home, func(r *manifest.Registry) error {
		i := slices.IndexFunc(r.Plugins, func(p manifest.Installed) bool { return p.ID == m.ID })
		switch {
		case i >= 0:
			prev := r.Plugins[i]
			res.Previous = &prev
			switch {
			case prev.Hash != plan.Hash:
				res.Outcome = Upgraded
			case intact(home, prev):
				res.Outcome, res.Entry, res.Previous = Unchanged, prev, nil
				return errNothingToDo
			default:
				res.Outcome = Repaired
			}
		case len(r.Plugins) >= manifest.MaxInstalled:
			return fmt.Errorf("plugin add: %d plugins are installed, the most there can be: remove one first", manifest.MaxInstalled)
		}
		if err := os.MkdirAll(data, 0o700); err != nil {
			return fmt.Errorf("plugin add: %w", err)
		}
		if err := writeCopy(dir, plan.Files, plan.Hash); err != nil {
			return err
		}
		wrote = true
		if i >= 0 {
			r.Plugins[i] = entry
		} else {
			r.Plugins = append(r.Plugins, entry)
		}
		return nil
	})
	switch {
	case errors.Is(err, errNothingToDo):
		return res, nil
	case err != nil:
		if wrote && (res.Previous == nil || res.Previous.Hash != plan.Hash) {
			_ = removeTree(dir) // the registry never named it
		}
		return Result{}, err
	}
	// Best effort. A copy that stays behind is never run, because only the
	// registry names what runs, and the next upgrade or remove clears it.
	if res.Previous != nil && res.Previous.Hash != plan.Hash {
		if old := manifest.InstallPath(home, m.ID, res.Previous.Hash); old != "" {
			_ = removeTree(old)
		}
	}
	return res, nil
}

// writeCopy writes files to dir: into a new directory beside it first, then
// renamed into place, so a failed write never leaves a half copy at dir. It
// then makes the copy read-only and hashes it again; a copy that does not
// hash to want is removed.
func writeCopy(dir string, files []manifest.TreeFile, want string) error {
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("plugin add: %w", err)
	}
	tmp, err := os.MkdirTemp(parent, ".add-")
	if err != nil {
		return fmt.Errorf("plugin add: %w", err)
	}
	defer func() { _ = removeTree(tmp) }() // no-op once renamed
	for _, f := range files {
		if !fs.ValidPath(f.Path) || f.Path == "." {
			return fmt.Errorf("plugin add: %q is not a path inside the plugin", f.Path)
		}
		full := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return fmt.Errorf("plugin add: %w", err)
		}
		if err := writeNew(full, f.Data); err != nil {
			return fmt.Errorf("plugin add: %w", err)
		}
	}
	if err := removeTree(dir); err != nil {
		return fmt.Errorf("plugin add: %w", err)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return fmt.Errorf("plugin add: %w", err)
	}
	if err := lockModes(dir, files); err != nil {
		_ = removeTree(dir)
		return fmt.Errorf("plugin add: %w", err)
	}
	got, err := manifest.HashDir(dir)
	if err != nil || got != want {
		_ = removeTree(dir)
		return errors.New("plugin add: the copy does not match the files that were read: nothing was installed")
	}
	return nil
}

// writeNew creates name, which must not exist, with data.
func writeNew(name string, data []byte) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// lockModes makes the copy read-only: files 0400 (0500 for an executable),
// directories 0500. Directories change last and deepest first, so each one
// is still writable while its children change.
func lockModes(dir string, files []manifest.TreeFile) error {
	var dirs []string
	for _, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(f.Path))
		mode := os.FileMode(0o400)
		if f.Exec {
			mode = 0o500
		}
		if err := os.Chmod(full, mode); err != nil {
			return err
		}
		for d := filepath.Dir(full); len(d) > len(dir); d = filepath.Dir(d) {
			if !slices.Contains(dirs, d) {
				dirs = append(dirs, d)
			}
		}
	}
	dirs = append(dirs, dir)
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	for _, d := range dirs {
		if err := os.Chmod(d, 0o500); err != nil { //nolint:gosec // a directory needs its search bit: 0500 is the read-only mode the ADR requires
			return err
		}
	}
	return nil
}

// removeTree deletes dir and everything below it, including a read-only copy.
// It never follows a symbolic link, and a path that does not exist is not an
// error.
func removeTree(dir string) error {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		// A read-only directory cannot be emptied: make each writable first.
		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(p, 0o700) //nolint:gosec // a directory needs its search bit, and this makes a read-only copy deletable
			}
			return nil
		})
	}
	return os.RemoveAll(dir)
}
