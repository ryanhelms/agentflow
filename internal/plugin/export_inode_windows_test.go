package plugin

import "os"

// InodeForTest has no inode to report on Windows: it is always 0, so the
// rewrite count stays 0 there.
func InodeForTest(os.FileInfo) uint64 { return 0 }
