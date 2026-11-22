//go:build darwin || linux

package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
	"time"
)

func ownedByEffectiveUser(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func openNonblocking(dir *os.Root, name string) (*os.File, error) {
	return dir.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}

// Lock the directory inode itself, so an interrupted process releases the
// lock and writers never have to create or remove a lock file.
func lockDirectory(ctx context.Context, dir *os.Root) (*os.File, error) {
	file, err := dir.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open cache directory for locking: %w", err)
	}
	// Keep the File reference alive while flock uses its descriptor.
	fd := int(file.Fd())
	for ctx.Err() == nil {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return file, nil
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, fmt.Errorf("lock cache directory: %w", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(20 * time.Millisecond):
		}
	}
	_ = file.Close()
	return nil, ctx.Err()
}
