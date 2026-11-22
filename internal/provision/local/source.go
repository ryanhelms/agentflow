package local

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// OpenManifest opens an ordinary, user-owned pin file in a trusted directory.
// Use a fixed filename supplied by the application.
func OpenManifest(home, name string) (*os.File, error) {
	if !filepath.IsLocal(name) || filepath.Base(name) != name {
		return nil, errors.New("invalid manifest name")
	}
	dir, err := openCache(home)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return openRegular(dir, name, true)
}

// OpenRawFile opens a regular local source without blocking on a special file.
// Install verifies the bytes before publishing, regardless of source ownership.
func OpenRawFile(path string) (*os.File, error) {
	dir, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return openRegular(dir, filepath.Base(path), false)
}

func openRegular(dir *os.Root, name string, requireOwner bool) (*os.File, error) {
	before, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("expected a regular file for %q", name)
	}
	if requireOwner && (!ownedByEffectiveUser(before) || before.Mode().Perm()&0o022 != 0) {
		return nil, fmt.Errorf("unsafe companion manifest %q: use a regular file owned by the current user without group or other write permission", name)
	}
	file, err := openNonblocking(dir, name)
	if err != nil {
		return nil, err
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || (requireOwner && !ownedByEffectiveUser(after)) {
		_ = file.Close()
		return nil, fmt.Errorf("file %q changed while opening", name)
	}
	return file, nil
}
