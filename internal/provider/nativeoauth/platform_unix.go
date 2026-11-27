//go:build !windows

package nativeoauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

func OpenBrowser(ctx context.Context, address string) error {
	return OpenBrowserEnv(ctx, address, nil)
}

// OpenBrowserEnv is OpenBrowser with the opener's environment given: nil
// inherits this process's. The console's sign-in link passes a fixed list
// so the caller's BROWSER, PATH, HOME or XDG_* cannot choose the program
// that receives it (xdg-open follows them).
func OpenBrowserEnv(ctx context.Context, address string, env []string) error {
	command := "/usr/bin/xdg-open"
	display := os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
	if env != nil {
		display = false
		for _, kv := range env {
			if strings.HasPrefix(kv, "DISPLAY=") || strings.HasPrefix(kv, "WAYLAND_DISPLAY=") {
				display = true
			}
		}
	}
	if runtime.GOOS == "darwin" {
		command = "/usr/bin/open"
	} else if !display {
		return errors.New("a graphical desktop is required for OAuth enrollment")
	}
	cmd := exec.CommandContext(ctx, command, address)
	cmd.Env = env
	return cmd.Run()
}

func tryLock(f *os.File) (bool, error) {
	err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EINTR) {
		return false, nil
	}
	return err == nil, err
}
