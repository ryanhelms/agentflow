//go:build unix

package harness

import "syscall"

// processAlive reports whether pid still runs.
func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
