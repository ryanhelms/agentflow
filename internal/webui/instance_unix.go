//go:build !windows

package webui

import (
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func tryInstanceLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return err == nil, err
}

func makePrivateDir(path string) error { return os.Mkdir(path, 0700) }

func privateMode(_ string, info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(stat.Uid) == int64(os.Geteuid()) && info.Mode().Perm()&0077 == 0
}

func directoryID(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() {
		return "", errors.New("expected a git directory")
	}
	return fmt.Sprintf("%x-%x", st.Dev, st.Ino), nil
}
