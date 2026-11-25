package nativeoauth

import (
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"time"
)

var errLock = errors.New("could not lock OAuth grant storage")

func UserLockDir(name string) (string, error) {
	// Every process using this OS user's credential store must share the lock,
	// regardless of checkout, AGENTFLOW_HOME or XDG configuration.
	u, err := user.Current()
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		return "", errLock
	}
	return filepath.Join(u.HomeDir, name), nil
}

func Lock(ctx context.Context, lockDir func() (string, error)) (*os.File, error) {
	dir, err := lockDir()
	if err != nil || os.MkdirAll(dir, 0700) != nil {
		return nil, errLock
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errLock
	}
	f, err := os.OpenFile(filepath.Join(dir, "grant.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errLock
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = f.Close()
			return nil, errors.Join(errLock, err)
		}
		ok, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, errLock
		}
		if ok {
			return f, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
}
