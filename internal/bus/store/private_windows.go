//go:build windows

package store

import (
	"fmt"
	"os"
)

// checkPrivateDir fails with ErrNotPrivate unless dir is a directory and not
// a reparse point. Windows has no mode bits: the owner-only DACL is
// inherited from the daemon's private registry, which the daemon checks
// before it opens the global store (see OpenGlobal).
func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("open global event store: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeIrregular != 0 {
		return fmt.Errorf("%w: %s", ErrNotPrivate, dir)
	}
	return nil
}
