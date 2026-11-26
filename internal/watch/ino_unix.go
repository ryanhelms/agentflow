//go:build unix

package watch

import (
	"os"
	"syscall"
)

// fileIno returns the inode number, which changes when git renames a lock
// file over the old file.
func fileIno(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino) //nolint:unconvert // Ino is uint32 on some platforms
	}
	return 0
}
