package nativeoauth

import (
	"context"
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// OpenBrowserEnv is OpenBrowser: ShellExecute starts no program from the
// environment's search path, so env is not used.
func OpenBrowserEnv(ctx context.Context, address string, _ []string) error {
	return OpenBrowser(ctx, address)
}

func OpenBrowser(ctx context.Context, address string) error {
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
