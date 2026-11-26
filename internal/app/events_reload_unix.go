//go:build unix

package app

import (
	"fmt"
	"os"
	"syscall"
)

// fileID identifies a file by device and inode, which change when an editor
// or git renames a new file over the old one.
func fileID(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%x-%x", st.Dev, st.Ino)
	}
	return ""
}
