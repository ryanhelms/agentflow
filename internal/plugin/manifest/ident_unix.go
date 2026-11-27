//go:build !windows

package manifest

import (
	"fmt"
	"os"
	"syscall"
)

// fileIdentity names the file behind info (device and inode), so a registry
// replaced by an atomic rename changes the revision even with the same size
// and time.
func fileIdentity(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return "0:0"
}
