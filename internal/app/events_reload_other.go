//go:build !unix

package app

import "os"

// fileID returns "": os.FileInfo carries no file ID on this platform, so the
// fingerprint relies on size and modification time.
func fileID(os.FileInfo) string { return "" }
