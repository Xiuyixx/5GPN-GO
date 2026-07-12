package netguard

import (
	"net"
	"testing"
)

func TestIsPublicIP(t *testing.T) {
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !IsPublicIP(net.ParseIP(raw)) {
			t.Errorf("public address rejected: %s", raw)
		}
	}
	for _, raw := range []string{
		"0.0.0.0", "10.0.0.1", "100.64.0.1", "127.0.0.1",
		"169.254.169.254", "172.16.0.1", "192.168.1.1", "::", "::1", "fe80::1", "fd00::1",
	} {
		if IsPublicIP(net.ParseIP(raw)) {
			t.Errorf("non-public address accepted: %s", raw)
		}
	}
}

func TestValidateHTTPURL(t *testing.T) {
	for _, raw := range []string{"https://example.com/list.txt", "http://example.com:8080/x"} {
		if _, err := ValidateHTTPURL(raw); err != nil {
			t.Errorf("valid URL %q rejected: %v", raw, err)
		}
	}
	for _, raw := range []string{
		"file:///etc/passwd", "https://user:pass@example.com/x", "//example.com/x", "https:///x",
	} {
		if _, err := ValidateHTTPURL(raw); err == nil {
			t.Errorf("unsafe URL %q accepted", raw)
		}
	}
}

func TestResolvePublicRejectsLiteralPrivateAddress(t *testing.T) {
	if _, err := ResolvePublic(t.Context(), nil, "169.254.169.254"); err == nil {
		t.Fatal("metadata address accepted")
	}
	ips, err := ResolvePublic(t.Context(), nil, "1.1.1.1")
	if err != nil || len(ips) != 1 || !ips[0].Equal(net.ParseIP("1.1.1.1")) {
		t.Fatalf("public literal: ips=%v err=%v", ips, err)
	}
}
