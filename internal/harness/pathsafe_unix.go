//go:build unix

package harness

import (
	"os"
	"syscall"
)

// dirUnsafe reports whether a PATH directory is writable by group or others
// without the sticky bit, or owned by neither this user nor root.
func dirUnsafe(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return true
	}
	mode := info.Mode()
	if mode.Perm()&0o022 != 0 && mode&os.ModeSticky == 0 {
		return true
	}
	return !ownedByUserOrRoot(info)
}

// fileUnsafe reports whether a found file (links resolved) is writable by
// group or others, or owned by neither this user nor root.
func fileUnsafe(target string) bool {
	info, err := os.Stat(target)
	if err != nil {
		return true
	}
	if info.Mode().Perm()&0o022 != 0 {
		return true
	}
	return !ownedByUserOrRoot(info)
}

// homeUnsafe reports whether the agentflow home is not owned by this user
// or is writable by group or others. A home that does not exist yet is
// safe: nothing can be read from it.
func homeUnsafe(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil {
		return !os.IsNotExist(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != os.Getuid() {
		return true
	}
	return info.Mode().Perm()&0o022 != 0
}

func ownedByUserOrRoot(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return st.Uid == 0 || int(st.Uid) == os.Getuid()
}

func fileID(info os.FileInfo) (dev, ino uint64) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), st.Ino //nolint:unconvert // Dev is int32 on darwin, uint64 on linux
	}
	return 0, 0
}
