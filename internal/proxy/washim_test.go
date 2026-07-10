package proxy

import (
	"encoding/hex"
	"net"
	"testing"
)

func TestClassifyKnownWA(t *testing.T) {
	frame, _ := hex.DecodeString("454400010102")
	route, ver := Classify(frame)
	if route != RouteWhatsApp || ver != "known" {
		t.Fatalf("want (whatsapp,known) got (%s,%s)", route, ver)
	}
}

func TestClassifyKnownED(t *testing.T) {
	frame, _ := hex.DecodeString("574106031234")
	route, ver := Classify(frame)
	if route != RouteWhatsApp || ver != "known" {
		t.Fatalf("want (whatsapp,known) got (%s,%s)", route, ver)
	}
}

func TestClassifyNewWA(t *testing.T) {
	// "ED" prefix (0x45 0x44) but not one of the known 4-byte handshakes.
	route, ver := Classify([]byte{0x45, 0x44, 0xff, 0xff})
	if route != RouteWhatsApp || ver != "new" {
		t.Fatalf("want (whatsapp,new) got (%s,%s)", route, ver)
	}
	// Only 2 bytes of an ED prefix should still classify.
	route, ver = Classify([]byte{0x45, 0x44})
	if route != RouteWhatsApp {
		t.Fatalf("want whatsapp got %s", route)
	}
	_ = ver
}

func TestClassifyBackendDefault(t *testing.T) {
	// TLS ClientHello starts with 0x16 0x03.
	route, _ := Classify([]byte{0x16, 0x03, 0x01, 0x00})
	if route != RouteBackend {
		t.Fatalf("want backend got %s", route)
	}
	// Short-frame fail-open (fewer than 2 bytes goes to backend).
	route, _ = Classify([]byte{0x45})
	if route != RouteBackend {
		t.Fatalf("<2 bytes should route to backend, got %s", route)
	}
}

func TestSourceAllowed(t *testing.T) {
	cfg := DefaultWAShimConfig()
	cases := []struct {
		ip      string
		allowed bool
	}{
		{"172.22.1.5", true},
		{"127.0.0.5", true},
		{"8.8.8.8", false},
		{"::1", false}, // wa-shim's default allow list is IPv4-only.
		{"not-an-ip", false},
	}
	for _, c := range cases {
		if got := SourceAllowed(cfg, c.ip); got != c.allowed {
			t.Errorf("SourceAllowed(%q) = %v, want %v", c.ip, got, c.allowed)
		}
	}
}

func TestDefaultsMatchPython(t *testing.T) {
	c := DefaultWAShimConfig()
	if c.Listen != "0.0.0.0" || c.Port != 443 || c.Backend != "127.0.0.1:8443" ||
		c.WAHost != "g.whatsapp.net" || c.WAPort != 443 ||
		c.PeekTimeout.Seconds() != 3 || c.ConnectTimeout.Seconds() != 8 ||
		c.DNSTTL.Seconds() != 60 || c.MaxConn != 8192 {
		t.Fatalf("wa-shim defaults drifted from wa-shim.py contract: %+v", c)
	}
	if len(c.Resolvers) != 2 || c.Resolvers[0] != "1.1.1.1" || c.Resolvers[1] != "8.8.8.8" {
		t.Fatalf("resolver defaults drifted: %v", c.Resolvers)
	}
	// Self IP baseline must include the four Python defaults.
	for _, v := range []string{"127.0.0.1", "::1", "0.0.0.0", "::"} {
		if !c.SelfIPs[v] {
			t.Errorf("SelfIPs missing baseline %q", v)
		}
	}
	// Allow CIDR baseline covers the two Python defaults.
	if len(c.AllowCIDR) != 2 {
		t.Fatalf("want 2 default AllowCIDR, got %d", len(c.AllowCIDR))
	}
	if !SourceAllowed(c, "172.22.100.1") || !SourceAllowed(c, "127.0.0.5") {
		t.Errorf("default allow CIDRs must accept 172.22.0.0/16 + 127.0.0.0/8")
	}
}

// Compile-time sanity: cfg values are usable to build a listen address.
func TestNetJoinHostPort(t *testing.T) {
	c := DefaultWAShimConfig()
	addr := net.JoinHostPort(c.Listen, "443")
	if addr != "0.0.0.0:443" {
		t.Fatalf("unexpected join: %s", addr)
	}
}
