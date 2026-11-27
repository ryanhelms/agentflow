//go:build windows

package aisession

import "io/fs"

// links: hard link counts are not checked on Windows.
func links(fs.FileInfo) uint64 { return 1 }
