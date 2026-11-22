package rovooauth

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func openBrowser(ctx context.Context, address string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	uri, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, nil, uri, nil, nil, windows.SW_SHOWNORMAL)
}

func tryLock(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
