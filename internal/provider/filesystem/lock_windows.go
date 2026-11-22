package filesystem

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
	"unsafe"
)

const readFlags = os.O_RDONLY

const lockName = ".devflow-write.lock"

func lockHandle(root *os.Root) (*os.File, error) {
	info, err := root.Lstat(lockName)
	if err == nil && !info.Mode().IsRegular() {
		return nil, errors.New(".devflow-write.lock must be a regular file")
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return root.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0600)
}

// LockFileEx on the first byte of a persistent file. The kernel drops the lock
// when its handle closes, including when a process is killed.
var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func tryLock(f *os.File) (bool, error) {
	const failImmediatelyExclusive = 1 | 2
	const lockViolation = syscall.Errno(33)
	var offset syscall.Overlapped
	//nolint:gosec // LockFileEx requires a pointer to OVERLAPPED for this synchronous call.
	ok, _, err := lockFileEx.Call(f.Fd(), failImmediatelyExclusive, 0, 1, 0, uintptr(unsafe.Pointer(&offset)))
	if ok != 0 {
		return true, nil
	}
	if errors.Is(err, lockViolation) {
		return false, nil
	}
	return false, err
}

// Windows permits FlushFileBuffers on the staged file, but not the directory.
func syncDirectory(_ *os.File) error { return nil }
