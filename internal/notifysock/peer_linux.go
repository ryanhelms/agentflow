//go:build linux

package notifysock

import (
	"net"

	"golang.org/x/sys/unix"
)

// socketPeerUID reads SO_PEERCRED of a Unix socket connection.
func socketPeerUID(c net.Conn) (uid int, reported bool, err error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, false, nil
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, false, err
	}
	if credErr != nil {
		return 0, false, credErr
	}
	return int(cred.Uid), true, nil
}
