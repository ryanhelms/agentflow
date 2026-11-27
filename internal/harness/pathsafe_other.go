//go:build !unix

package harness

import "os"

// On Windows the permission checks of the PATH table are Unix only; a
// .cmd or .bat file is refused instead (shim).
func dirUnsafe(string) bool  { return false }
func fileUnsafe(string) bool { return false }

// homeUnsafe: the owner and mode checks are Unix only.
func homeUnsafe(string) bool { return false }

func fileID(os.FileInfo) (dev, ino uint64) { return 0, 0 }
