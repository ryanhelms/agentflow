package notifysock

import (
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
)

// Listen opens the daemon's endpoint for the registry prefix prefix: a
// Unix socket at UnixPath(prefix) with mode 0600, or loopback TCP on
// Windows and when that path is too long. The caller holds the instance
// lock, so a socket file already at the path is a stale one from a daemon
// that exited without cleaning up, and Listen removes it. Anything else at
// the path is an error. The returned Endpoint has no Key.
func Listen(prefix string) (net.Listener, Endpoint, error) {
	path := UnixPath(prefix)
	if runtime.GOOS == "windows" || path == "" {
		l, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return nil, Endpoint{}, fmt.Errorf("open notify socket: %w", err)
		}
		return l, Endpoint{Network: "tcp", Address: l.Addr().String()}, nil
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, Endpoint{}, fmt.Errorf("open notify socket: %s exists and is not a socket", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, Endpoint{}, fmt.Errorf("remove stale notify socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, Endpoint{}, fmt.Errorf("open notify socket: %w", err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, Endpoint{}, fmt.Errorf("open notify socket: %w", err)
	}
	// The registry directory is 0700 already; the mode keeps the socket
	// private even if the directory's mode is later loosened.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		return nil, Endpoint{}, fmt.Errorf("restrict notify socket: %w", err)
	}
	return l, Endpoint{Network: "unix", Address: path}, nil
}
