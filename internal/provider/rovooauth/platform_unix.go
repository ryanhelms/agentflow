//go:build !windows

package rovooauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

func openBrowser(ctx context.Context, address string) error {
	command := "/usr/bin/xdg-open"
	if runtime.GOOS == "darwin" {
		command = "/usr/bin/open"
	} else if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("a graphical desktop is required for Rovo enrollment")
	}
	return exec.CommandContext(ctx, command, address).Run()
}

func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return err == nil, err
}
