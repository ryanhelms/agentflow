package manifest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// FileSource is the Source over the registry file of an agentflow home:
// <home>/plugins/installed.json. It is stateless; every call reads the file,
// so a plugin add from another process shows at once.
type FileSource struct {
	home     string
	builtins func() []Installed
}

var _ Source = (*FileSource)(nil)

// NewFileSource returns the Source of the agentflow home (config.HomeDir()).
func NewFileSource(home string) *FileSource { return &FileSource{home: home} }

// WithBuiltins adds the plugins compiled into agentflow to what Installed
// returns. The host passes its registered builtins here; the manifest
// package cannot import the host. A builtin's ID shadows a registry entry of
// the same ID.
func (s *FileSource) WithBuiltins(fn func() []Installed) *FileSource {
	s.builtins = fn
	return s
}

// Installed returns the registry's plugins and the builtins, sorted by ID.
func (s *FileSource) Installed(ctx context.Context) ([]Installed, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reg, err := ReadRegistry(s.home)
	if err != nil {
		return nil, err
	}
	out := reg.Plugins
	if s.builtins != nil {
		built := s.builtins()
		out = slices.DeleteFunc(out, func(p Installed) bool {
			return slices.ContainsFunc(built, func(b Installed) bool { return b.ID == p.ID })
		})
		out = append(out, built...)
		slices.SortFunc(out, func(a, b Installed) int { return strings.Compare(a.ID, b.ID) })
	}
	return out, nil
}

// Verify hashes the installed copy of p again and checks its manifest. It
// returns an error that wraps ErrHashMismatch when the copy is not where the
// registry says, cannot be read, hashes differently or holds another
// manifest than the registry entry; and a *ValidationError when the copy's
// manifest no longer validates. A builtin always verifies.
func (s *FileSource) Verify(ctx context.Context, p Installed) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.Tier == TierBuiltin {
		return nil
	}
	want := InstallPath(s.home, p.ID, p.Hash)
	if want == "" || filepath.Clean(p.Dir) != want {
		return fmt.Errorf("plugin %q is not installed where agentflow keeps it: %w", p.ID, ErrHashMismatch)
	}
	files, err := ReadTree(want)
	if err != nil {
		return fmt.Errorf("plugin %q: %w: %w", p.ID, err, ErrHashMismatch)
	}
	if got := HashFiles(files); got != p.Hash {
		return fmt.Errorf("plugin %q: %w", p.ID, ErrHashMismatch)
	}
	i := slices.IndexFunc(files, func(f TreeFile) bool { return f.Path == FileName })
	if i < 0 {
		return fmt.Errorf("plugin %q has no %s: %w", p.ID, FileName, ErrHashMismatch)
	}
	m, err := Load(files[i].Data)
	if err != nil {
		return err
	}
	if !sameManifest(m, p.Manifest) {
		return fmt.Errorf("plugin %q: the manifest differs from the registry entry: %w", p.ID, ErrHashMismatch)
	}
	return nil
}

// sameManifest compares two manifests as JSON, which is how the registry
// stores one: numbers that went through it compare equal.
func sameManifest(a, b Manifest) bool {
	x, errA := json.Marshal(a)
	y, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

// Revision changes whenever Installed would return something else: it is the
// registry file's size, modification time and identity, or "absent".
func (s *FileSource) Revision() string {
	info, err := os.Stat(RegistryPath(s.home))
	if err != nil {
		return "absent"
	}
	return fmt.Sprintf("%d-%d-%s", info.Size(), info.ModTime().UnixNano(), fileIdentity(info))
}
