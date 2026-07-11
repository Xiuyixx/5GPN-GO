package ios

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRenderRoundTrip(t *testing.T) {
	body, err := Render(ProfileParams{
		Domain:      "dot.example.com",
		UUID:        "00000000-0000-0000-0000-000000000001",
		PayloadUUID: "00000000-0000-0000-0000-000000000002",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		"<key>ServerName</key>", "dot.example.com",
		"com.5gpn.dot", "5GPN DoT",
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered profile missing %q\n--- body ---\n%s", want, s)
		}
	}
}

func TestRenderRejectsMissingDomain(t *testing.T) {
	_, err := Render(ProfileParams{UUID: "a", PayloadUUID: "b"})
	if err == nil {
		t.Fatal("expected error for empty domain")
	}
}

// TestRenderOnDemandAndFallback covers plan §4 Phase 8 / AC-I5: the
// mobileconfig must gain an OnDemandRules array (WiFi + Cellular,
// Action: Connect) plus a second DNSSettings payload for FallbackDoT so an
// iPhone can auto-switch upstream if the primary VPS-hosted DoT server
// goes down.
func TestRenderOnDemandAndFallback(t *testing.T) {
	body, err := Render(ProfileParams{
		Domain:              "dot.example.com",
		UUID:                "00000000-0000-0000-0000-000000000001",
		PayloadUUID:         "00000000-0000-0000-0000-000000000002",
		OnDemand:            true,
		FallbackDoT:         "1.1.1.1",
		FallbackPayloadUUID: "00000000-0000-0000-0000-000000000003",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, want := range []string{
		"<key>OnDemandRules</key>",
		"<string>Connect</string>",
		"<string>WiFi</string>",
		"<string>Cellular</string>",
		"<string>1.1.1.1</string>",
		"00000000-0000-0000-0000-000000000003",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered profile missing %q\n--- body ---\n%s", want, s)
		}
	}
	// Two DNSSettings payloads: primary + fallback.
	if got := strings.Count(s, "<key>DNSSettings</key>"); got != 2 {
		t.Errorf("want 2 DNSSettings dicts, got %d", got)
	}
	// Only ONE OnDemandRules block — on the primary payload. Duplicating
	// it on the fallback confuses iOS (Apple docs: only one DNS proxy /
	// DNS settings profile is active at a time; a second OnDemand block
	// on the fallback creates ambiguous priority) and was seen in the
	// wild causing the fallback to be preferred when it shouldn't be.
	if got := strings.Count(s, "<key>OnDemandRules</key>"); got != 1 {
		t.Errorf("want 1 OnDemandRules block (primary only), got %d", got)
	}
	// Primary must advertise DoH so it survives GFW / carrier DPI on
	// :853 — see plan §4 Phase 8 rev-2. Fallback keeps DoT for
	// backwards-compat with older clients + when :443 is throttled.
	if !strings.Contains(s, "<string>HTTPS</string>") {
		t.Errorf("primary payload should be DoH (DNSProtocol=HTTPS)\n%s", s)
	}
	if !strings.Contains(s, "https://dot.example.com/dns-query") {
		t.Errorf("primary payload missing ServerURL\n%s", s)
	}
	validatePlistIfPossible(t, body)
}

func TestRenderWithoutOnDemandOrFallbackOmitsExtras(t *testing.T) {
	body, err := Render(ProfileParams{
		Domain:      "dot.example.com",
		UUID:        "00000000-0000-0000-0000-000000000001",
		PayloadUUID: "00000000-0000-0000-0000-000000000002",
	})
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	if strings.Contains(s, "OnDemandRules") {
		t.Errorf("OnDemandRules should be absent when OnDemand=false:\n%s", s)
	}
	if got := strings.Count(s, "<key>DNSSettings</key>"); got != 1 {
		t.Errorf("want 1 DNSSettings dict, got %d", got)
	}
	validatePlistIfPossible(t, body)
}

func TestRenderRejectsFallbackWithoutUUID(t *testing.T) {
	_, err := Render(ProfileParams{
		Domain:      "dot.example.com",
		UUID:        "a",
		PayloadUUID: "b",
		FallbackDoT: "1.1.1.1",
	})
	if err == nil {
		t.Fatal("expected error when FallbackDoT is set without FallbackPayloadUUID")
	}
}

// validatePlistIfPossible shells out to macOS's plutil (when available) to
// confirm the rendered document is well-formed plist XML — the same check
// called out in the plan's Phase 8 acceptance step. It's a no-op on
// platforms without plutil (e.g. Linux CI) rather than a hard dependency.
func validatePlistIfPossible(t *testing.T, body []byte) {
	t.Helper()
	if _, err := exec.LookPath("plutil"); err != nil {
		t.Skip("plutil not available; skipping plist structural validation")
	}
	f, err := os.CreateTemp(t.TempDir(), "*.mobileconfig")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(body); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	out, err := exec.Command("plutil", "-lint", f.Name()).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil -lint failed: %v\n%s", err, out)
	}
}

func TestServeConnGetProfile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ios-dot.mobileconfig"), []byte("PROFILE_BYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = ServeConn(conn, dir, DefaultRoutes(), 5*time.Second)
		_ = conn.Close()
	}()

	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(conn, "GET /ios-dot.mobileconfig HTTP/1.1\r\nHost: x\r\n\r\n")

	r := bufio.NewReader(conn)
	statusLine, _ := r.ReadString('\n')
	if !strings.Contains(statusLine, "200 OK") {
		t.Fatalf("want 200 OK, got %q", statusLine)
	}
	rest := &bytes.Buffer{}
	_, _ = rest.ReadFrom(r)
	if !strings.Contains(rest.String(), "PROFILE_BYTES") {
		t.Fatalf("body missing PROFILE_BYTES: %s", rest.String())
	}
	if !strings.Contains(rest.String(), "application/x-apple-aspen-config") {
		t.Fatalf("wrong content-type: %s", rest.String())
	}
}

func TestServeConnRejectsPost(t *testing.T) {
	dir := t.TempDir()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		fmt.Fprint(client, "POST / HTTP/1.1\r\nHost: x\r\n\r\n")
	}()
	if err := ServeConn(server, dir, DefaultRoutes(), time.Second); err == nil {
		t.Fatal("expected error on POST")
	}
}

func TestServeConn404(t *testing.T) {
	dir := t.TempDir()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()

	go func() {
		c, _ := ln.Accept()
		_ = ServeConn(c, dir, DefaultRoutes(), 2*time.Second)
		_ = c.Close()
	}()
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	fmt.Fprint(c, "GET /does-not-exist HTTP/1.1\r\nHost: x\r\n\r\n")
	line, _ := bufio.NewReader(c).ReadString('\n')
	if !strings.Contains(line, "404") {
		t.Fatalf("want 404, got %q", line)
	}
}
