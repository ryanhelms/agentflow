//go:build !windows

// Package filelock serializes updates across local processes.
package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Exclusive waits for a kernel lock. Closing f releases it.
func Exclusive(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

// TryExclusive reports whether f was locked without waiting for another holder.
func TryExclusive(f *os.File) (bool, error) {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) {
			return false, nil
		}
		return err == nil, err
	}
}
