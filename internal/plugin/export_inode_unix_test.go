//go:build !windows

package plugin

import (
	"os"
	"syscall"
)

// InodeForTest is the inode of st, to count rewrites.
func InodeForTest(st os.FileInfo) uint64 { return st.Sys().(*syscall.Stat_t).Ino }
