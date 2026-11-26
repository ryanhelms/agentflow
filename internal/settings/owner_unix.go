//go:build !windows

package settings

import (
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether fi belongs to the user this process
// runs as.
func ownedByCurrentUser(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int64(st.Uid) == int64(os.Geteuid())
}
