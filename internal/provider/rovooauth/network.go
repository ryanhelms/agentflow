package rovooauth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
)

func newClient() *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		addr, err := netip.ParseAddrPort(address)
		if err != nil || addr.Port() != 443 || !publicIP(addr.Addr()) {
			return errors.New("connection to Rovo requires a public HTTPS destination")
		}
		return nil
	}}
	return &http.Client{Transport: &http.Transport{
		DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: time.Minute,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxResponseHeaderBytes: 64 << 10,
	}, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
func opaque(value string) bool {
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

// The value returned to the MCP transport must fit RFC 6750 header syntax.
var bearerSyntax = regexp.MustCompile(`^[A-Za-z0-9._~+/-]+=*$`)

type tokenResponse struct {
	Access    string  `json:"access_token"`
	Refresh   string  `json:"refresh_token"`
	Type      string  `json:"token_type"`
	Scope     *string `json:"scope"`
	ExpiresIn int64   `json:"expires_in"`
	Error     string  `json:"error"`
	expiry    time.Time
}

func (e *enrollment) exchange(ctx context.Context, clientID string, form url.Values) (*tokenResponse, error) {
	form.Set("resource", Resource)
	form.Set("client_id", clientID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errToken
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	started := time.Now()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, errToken
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != http.StatusOK || media != "application/json" {
		return nil, errToken
	}
	const maxBody = 64 << 10
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	var t tokenResponse
	if err != nil || len(body) > maxBody || json.Unmarshal(body, &t) != nil || t.Error != "" ||
		!strings.EqualFold(t.Type, "Bearer") || !bearerSyntax.MatchString(t.Access) || (t.Refresh != "" && !opaque(t.Refresh)) ||
		(t.Scope != nil && !sameScopes(*t.Scope)) || t.ExpiresIn <= 0 || t.ExpiresIn > 1<<31 {
		return nil, errToken
	}
	t.expiry = started.Add(time.Duration(t.ExpiresIn) * time.Second)
	if !time.Now().Before(t.expiry) {
		return nil, errToken
	}
	return &t, nil
}
