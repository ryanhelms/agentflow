// Package filelock serializes updates across local processes.
package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// Exclusive locks the first byte. Windows releases the lock when f is closed.
func Exclusive(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &windows.Overlapped{})
}

// TryExclusive reports whether f was locked without waiting for another holder.
func TryExclusive(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
