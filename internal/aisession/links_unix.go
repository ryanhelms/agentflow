//go:build !windows

package aisession

import (
	"io/fs"
	"syscall"
)

// links is the file's hard link count.
func links(info fs.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return widen(st.Nlink)
	}
	return 1
}

// widen: Nlink is uint16, uint32 or uint64 depending on the platform.
func widen[T ~uint16 | ~uint32 | ~uint64](v T) uint64 { return uint64(v) }
