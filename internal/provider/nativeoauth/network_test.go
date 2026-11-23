package nativeoauth

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestNetworkPolicy(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "::ffff:127.0.0.1", "10.0.1.2", "100.64.0.2", "169.254.169.254", "::1", "::", "fe80::1", "fc00::1", "2002:7f00:1::", "224.0.0.1"} {
		if publicIP(netip.MustParseAddr(ip)) {
			t.Errorf("accepted %s", ip)
		}
	}
	if !publicIP(netip.MustParseAddr("1.1.1.1")) || !publicIP(netip.MustParseAddr("2606:4700:4700::1111")) {
		t.Fatal("rejected public IP")
	}
	if NewClient().Transport.(*http.Transport).Proxy != nil {
		t.Fatal("ambient proxy enabled")
	}
}
