//go:build !windows

package store

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivateDir fails with ErrNotPrivate unless dir is a directory, not a
// symbolic link, owned by the effective user, with no group or other
// permission bits.
func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("open global event store: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || !ok || int64(st.Uid) != int64(os.Geteuid()) || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%w: %s", ErrNotPrivate, dir)
	}
	return nil
}
