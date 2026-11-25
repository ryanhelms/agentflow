package rovooauth

import (
	"context"
	"errors"
	"net"

	"github.com/nscaledev/agentflow/internal/provider/nativeoauth"
)

const callbackPath = "/oauth/rovo/callback"

func listenCallback(ctx context.Context) (net.Listener, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot listen on the Rovo loopback callback")
	}
	return ln, nil
}

func waitCallback(ctx context.Context, ln net.Listener, state string, requireIssuer bool, launch func() error) (string, error) {
	return nativeoauth.WaitCallback(ctx, ln, ln.Addr().String(), callbackPath, issuer, state, requireIssuer, launch)
}
