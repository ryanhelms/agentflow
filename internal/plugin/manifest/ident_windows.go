//go:build windows

package manifest

import "os"

// fileIdentity has no inode on Windows: size and modification time stand in.
func fileIdentity(os.FileInfo) string { return "0:0" }
