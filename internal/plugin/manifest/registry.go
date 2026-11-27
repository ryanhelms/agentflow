package manifest

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ryanhelms/agentflow/internal/filelock"
)

// maxRegistryBytes bounds the registry file: MaxInstalled manifests of
// MaxManifestBytes each, with room for the rest of each entry.
const maxRegistryBytes = MaxInstalled*MaxManifestBytes + 1<<20

// RegistryVersion is the only registry format.
const RegistryVersion = 1

// PluginsPath is <home>/plugins, the directory that holds everything.
func PluginsPath(home string) string { return filepath.Join(home, PluginsDir) }

// RegistryPath is <home>/plugins/installed.json.
func RegistryPath(home string) string { return filepath.Join(PluginsPath(home), RegistryFile) }

// InstallPath is the installed copy of plugin id with content hash:
// <home>/plugins/<id>/<first 16 hex of the hash>. It is empty when id or
// hash is not valid, so a caller never builds a path from either.
func InstallPath(home, id, hash string) string {
	prefix := HashPrefix(hash)
	if prefix == "" || !idRe.MatchString(id) {
		return ""
	}
	return filepath.Join(PluginsPath(home), id, prefix)
}

// RootPath is <home>/plugins/<id>: everything of one plugin, its installed
// copies and its data. It is empty when id is not valid, so a caller never
// builds a path to delete from an ID it did not check.
func RootPath(home, id string) string {
	if !idRe.MatchString(id) {
		return ""
	}
	return filepath.Join(PluginsPath(home), id)
}

// DataPath is <home>/plugins/<id>/data, the plugin's own writable place.
func DataPath(home, id string) string {
	if !idRe.MatchString(id) {
		return ""
	}
	return filepath.Join(PluginsPath(home), id, DataDir)
}

// ReadRegistry reads the registry of home. A missing file is an empty
// registry. The plugins come back sorted by ID.
func ReadRegistry(home string) (Registry, error) {
	f, err := os.Open(RegistryPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return Registry{Version: RegistryVersion, Plugins: []Installed{}}, nil
	}
	if err != nil {
		return Registry{}, fmt.Errorf("plugin registry: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRegistryBytes+1))
	if err != nil {
		return Registry{}, fmt.Errorf("plugin registry: %w", err)
	}
	if len(data) > maxRegistryBytes {
		return Registry{}, errors.New("plugin registry: the file is too large")
	}
	var reg Registry
	if err := json.Unmarshal(data, &reg); err != nil {
		return Registry{}, fmt.Errorf("plugin registry: %w", err)
	}
	if reg.Version != RegistryVersion {
		return Registry{}, fmt.Errorf("plugin registry: version %d is not supported by this agentflow", reg.Version)
	}
	if reg.Plugins == nil {
		reg.Plugins = []Installed{}
	}
	slices.SortFunc(reg.Plugins, func(a, b Installed) int { return strings.Compare(a.ID, b.ID) })
	for i := 1; i < len(reg.Plugins); i++ {
		if reg.Plugins[i].ID == reg.Plugins[i-1].ID {
			return Registry{}, fmt.Errorf("plugin registry: %q is listed twice", reg.Plugins[i].ID)
		}
	}
	return reg, nil
}

// UpdateRegistry changes the registry under its lock: it reads the current
// registry, calls fn, and replaces the file atomically with the result (a
// temporary file in the same directory, fsync, rename), mode 0600, in a
// directory of mode 0700. When fn returns an error nothing is written. A
// concurrent update waits for the lock, so two plugin add calls never lose
// each other's entry.
func UpdateRegistry(home string, fn func(*Registry) error) error {
	dir := PluginsPath(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	lock, err := os.OpenFile(RegistryPath(home)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	defer lock.Close()
	if err := filelock.Exclusive(lock); err != nil {
		return fmt.Errorf("plugin registry: cannot lock: %w", err)
	}
	reg, err := ReadRegistry(home)
	if err != nil {
		return err
	}
	if err := fn(&reg); err != nil {
		return err
	}
	reg.Version = RegistryVersion
	slices.SortFunc(reg.Plugins, func(a, b Installed) int { return strings.Compare(a.ID, b.ID) })
	if reg.Plugins == nil {
		reg.Plugins = []Installed{}
	}
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	return writeFileAtomic(RegistryPath(home), append(data, '\n'))
}

func writeFileAtomic(target string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(target), ".installed-*")
	if err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	if err := os.Rename(f.Name(), target); err != nil {
		return fmt.Errorf("plugin registry: %w", err)
	}
	return nil
}
