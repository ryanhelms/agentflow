//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package filesystem

import (
	"errors"
	"os"
	"syscall"
)

const readFlags = os.O_RDONLY | syscall.O_NONBLOCK

// Each open gets its own file description, including within the same process.
// Closing it releases the kernel lock, including on abnormal process exit.
func lockHandle(root *os.Root) (*os.File, error) { return root.Open(".") }

func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
		return false, nil
	}
	return err == nil, err
}

func syncDirectory(f *os.File) error { return f.Sync() }
