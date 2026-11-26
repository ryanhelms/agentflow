//go:build !unix

package watch

import "os"

// fileIno returns 0: os.FileInfo carries no file ID on this platform, so
// the polling Source relies on mtime, size and, for small files, content.
func fileIno(os.FileInfo) uint64 { return 0 }
