//go:build !linux && !darwin

package notifysock

import "net"

// socketPeerUID reports nothing on platforms without a peer-credential
// call we use. Windows always uses loopback TCP; there the handshake key,
// read from the user's private registry, is the only check.
func socketPeerUID(net.Conn) (uid int, reported bool, err error) { return 0, false, nil }
