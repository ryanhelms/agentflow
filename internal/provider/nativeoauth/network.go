// Package nativeoauth contains the local browser, grant lock and HTTPS transport
// shared by Agentflow-owned OAuth enrollments.
package nativeoauth

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"
)

// NewTransport applies the same public HTTPS dial policy to MCP and OAuth.
func NewTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		addr, err := netip.ParseAddrPort(address)
		if err != nil || addr.Port() != 443 || !publicIP(addr.Addr()) {
			return errors.New("connection for OAuth requires a public HTTPS destination")
		}
		return nil
	}}
	return &http.Transport{
		DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: time.Minute,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxResponseHeaderBytes: 64 << 10,
	}
}

func NewClient() *http.Client {
	return &http.Client{Transport: NewTransport(), Timeout: 20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func publicIP(addr netip.Addr) bool {
	ip := addr.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || (ip.Is6() && ip.As16()[0]&0xe0 != 0x20) {
		return false
	}
	for _, reserved := range reservedPrefixes {
		if reserved.Contains(ip) {
			return false
		}
	}
	return true
}

// RFC 6749 VSCHAR values, constrained to printable non-space ASCII.
func Opaque(value string) bool {
	if value == "" || len(value) > 32<<10 {
		return false
	}
	for i := range len(value) {
		if value[i] < '!' || value[i] > '~' {
			return false
		}
	}
	return true
}
