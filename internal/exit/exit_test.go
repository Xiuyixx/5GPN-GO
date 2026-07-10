package exit

import "testing"

func TestParseSSStandardCreds(t *testing.T) {
	// aes-256-gcm:secret base64-encoded
	uri := "ss://YWVzLTI1Ni1nY206c2VjcmV0@example.com:8388#Node"
	p, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "ss" || p["server"] != "example.com" || p["port"].(int) != 8388 ||
		p["cipher"] != "aes-256-gcm" || p["password"] != "secret" {
		t.Fatalf("bad ss parse: %+v", p)
	}
}

func TestParseVMess(t *testing.T) {
	// {"add":"h","port":"443","id":"u","aid":"0","net":"ws","tls":"tls","host":"h","path":"/p"}
	uri := "vmess://eyJhZGQiOiJoIiwicG9ydCI6IjQ0MyIsImlkIjoidSIsImFpZCI6IjAiLCJuZXQiOiJ3cyIsInRscyI6InRscyIsImhvc3QiOiJoIiwicGF0aCI6Ii9wIn0="
	p, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "vmess" || p["server"] != "h" || p["port"] != 443 || p["uuid"] != "u" || p["alterId"] != 0 {
		t.Fatalf("bad vmess: %+v", p)
	}
	if p["tls"] != true {
		t.Fatalf("vmess should have TLS set: %+v", p)
	}
	if p["network"] != "ws" {
		t.Fatalf("vmess should have ws transport: %+v", p)
	}
}

func TestParseTrojan(t *testing.T) {
	p, err := Parse("trojan://pw@example.com:443?sni=fake.example.com&allowInsecure=1&type=grpc&serviceName=myservice")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "trojan" || p["password"] != "pw" || p["port"] != 443 || p["skip-cert-verify"] != true {
		t.Fatalf("bad trojan: %+v", p)
	}
	if p["servername"] != "fake.example.com" {
		t.Fatalf("trojan sni wrong: %+v", p)
	}
	if p["network"] != "grpc" {
		t.Fatalf("trojan transport not grpc: %+v", p)
	}
}

func TestParseVLessReality(t *testing.T) {
	p, err := Parse("vless://uuid@example.com:443?security=reality&pbk=publickey&sid=short&fp=firefox&flow=xtls-rprx-vision")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "vless" || p["uuid"] != "uuid" || p["flow"] != "xtls-rprx-vision" {
		t.Fatalf("bad vless: %+v", p)
	}
	reality, ok := p["reality-opts"].(map[string]any)
	if !ok {
		t.Fatalf("missing reality-opts: %+v", p)
	}
	if reality["public-key"] != "publickey" || reality["short-id"] != "short" {
		t.Fatalf("reality fields wrong: %+v", reality)
	}
	if p["client-fingerprint"] != "firefox" {
		t.Fatalf("client fp wrong: %v", p["client-fingerprint"])
	}
}

func TestParseVLessRealityMissingPubkey(t *testing.T) {
	_, err := Parse("vless://uuid@example.com:443?security=reality")
	if err == nil {
		t.Fatal("expected error when reality is missing pbk")
	}
}

func TestParseHysteria2(t *testing.T) {
	for _, prefix := range []string{"hysteria2://", "hy2://"} {
		p, err := Parse(prefix + "pw@example.com:8443?sni=fake&insecure=1&obfs=salamander&obfs-password=xyz")
		if err != nil {
			t.Fatalf("Parse(%s): %v", prefix, err)
		}
		if p["type"] != "hysteria2" || p["password"] != "pw" || p["obfs"] != "salamander" || p["obfs-password"] != "xyz" {
			t.Fatalf("bad hysteria2 (%s): %+v", prefix, p)
		}
	}
}

func TestParseTUIC(t *testing.T) {
	p, err := Parse("tuic://uuid:pass@example.com:443?sni=fake&udp_relay_mode=quic&congestion_control=cubic&allow_insecure=1")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "tuic" || p["uuid"] != "uuid" || p["password"] != "pass" {
		t.Fatalf("bad tuic: %+v", p)
	}
	if p["udp-relay-mode"] != "quic" || p["congestion-controller"] != "cubic" || p["skip-cert-verify"] != true {
		t.Fatalf("bad tuic knobs: %+v", p)
	}
}

func TestParseAnyTLS(t *testing.T) {
	p, err := Parse("anytls://pw@example.com:443?insecure=true")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "anytls" || p["password"] != "pw" || p["skip-cert-verify"] != true {
		t.Fatalf("bad anytls: %+v", p)
	}
}

func TestParseSocks5(t *testing.T) {
	p, err := Parse("socks5://user:pass@example.com:1080")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "socks5" || p["username"] != "user" || p["password"] != "pass" || p["port"] != 1080 {
		t.Fatalf("bad socks5: %+v", p)
	}
}

func TestParseSocks5h(t *testing.T) {
	p, err := Parse("socks5h://example.com:1080")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "socks5" || p["port"] != 1080 {
		t.Fatalf("bad socks5h: %+v", p)
	}
}

func TestParseHTTPS(t *testing.T) {
	p, err := Parse("https://user:pass@example.com:8443")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p["type"] != "http" || p["port"] != 8443 || p["tls"] != true {
		t.Fatalf("bad https: %+v", p)
	}
}

func TestParseUnsupportedScheme(t *testing.T) {
	if _, err := Parse("shadowsocksr://blah"); err == nil {
		t.Fatal("expected unsupported scheme error")
	}
}
