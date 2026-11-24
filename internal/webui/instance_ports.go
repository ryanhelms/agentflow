package webui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"syscall"
)

func daemonIdentity(prefix string) (instance, error) {
	path := prefix + ".identity.json"
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		out := instance{Key: randomKey()}
		return out, writePrivateJSON(path, out)
	}
	if err != nil || !info.Mode().IsRegular() || !privateMode(path, info) || info.Size() > 1024 {
		return instance{}, errors.New("invalid private daemon identity file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return instance{}, err
	}
	var out instance
	err = json.Unmarshal(raw, &out)
	key, keyErr := base64.RawURLEncoding.DecodeString(out.Key)
	if err != nil || keyErr != nil || len(key) != 32 || out.Port < 0 || out.Port > 65535 {
		return instance{}, errors.New("invalid private daemon identity")
	}
	return out, nil
}

func listenPair(port int) (net.Listener, net.Listener, error) {
	v4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, nil, err
	}
	selected := v4.Addr().(*net.TCPAddr).Port
	v6, err := net.Listen("tcp6", net.JoinHostPort("::1", strconv.Itoa(selected)))
	if err == nil || errors.Is(err, syscall.EAFNOSUPPORT) || errors.Is(err, syscall.EADDRNOTAVAIL) || errors.Is(err, syscall.EPROTONOSUPPORT) {
		return v4, v6, nil
	}
	_ = v4.Close()
	return nil, nil, err
}

func listenDaemon(requested, remembered int) (net.Listener, net.Listener, error) {
	if requested > 0 {
		return listenPair(requested)
	}
	defaultPort := 80
	if requested < 0 {
		defaultPort = 0
	}
	return listenDefault(defaultPort, remembered)
}

func listenDefault(defaultPort, remembered int) (net.Listener, net.Listener, error) {
	ports := []int{}
	if defaultPort > 0 {
		// Keep the origin of the user's bookmarks and browser grant across
		// restarts, even when port 80 later becomes available.
		if remembered > 0 && remembered != defaultPort {
			ports = append(ports, remembered)
		}
		ports = append(ports, defaultPort)
	}
	// Retry an ephemeral choice if that port happens to belong to another
	// IPv6 service. Never launch a URL resolving to different programs on
	// 127.0.0.1 and ::1.
	ports = append(ports, 0, 0, 0, 0)
	var err error
	for _, port := range ports {
		var v4, v6 net.Listener
		v4, v6, err = listenPair(port)
		if err == nil {
			return v4, v6, nil
		}
		if !errors.Is(err, syscall.EADDRINUSE) && !errors.Is(err, syscall.EACCES) && !errors.Is(err, syscall.EPERM) {
			break
		}
	}
	return nil, nil, fmt.Errorf("open agentflow loopback listener: %w", err)
}
