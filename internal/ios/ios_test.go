package ios

import (
	"bufio"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
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
		"com.5gpn.dot", "5GPN Encrypted DNS",
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
// iPhone can auto-switch upstream if the primary VPS-hosted DoH server
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

func TestRenderEscapesDynamicXMLValues(t *testing.T) {
	body, err := Render(ProfileParams{
		Domain: "dns&<example>", DisplayName: `A&B <DNS>`, Identifier: `com.example&dns`,
		UUID: "outer&uuid", PayloadUUID: "payload<uuid>",
	})
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		if _, err := dec.Token(); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("rendered profile is not well-formed XML: %v\n%s", err, body)
		}
	}
	if strings.Contains(string(body), "<DNS>") || strings.Contains(string(body), "dns&<example>") {
		t.Fatalf("dynamic value was not escaped: %s", body)
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
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	errCh := make(chan error, 1)
	go func() {
		err := ServeConn(server, dir, DefaultRoutes(), 5*time.Second)
		_ = server.Close()
		errCh <- err
	}()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := fmt.Fprint(client, "GET /ios-dot.mobileconfig HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	r := bufio.NewReader(client)
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
	if err := <-errCh; err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
}

func TestServeConnRejectsPost(t *testing.T) {
	dir := t.TempDir()
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	errCh := make(chan error, 1)
	go func() {
		err := ServeConn(server, dir, DefaultRoutes(), time.Second)
		_ = server.Close()
		errCh <- err
	}()
	if _, err := fmt.Fprint(client, "POST / HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(client).ReadString('\n')
	if !strings.Contains(line, "400") {
		t.Fatalf("want 400, got %q", line)
	}
	if err := <-errCh; err == nil {
		t.Fatal("expected error on POST")
	}
}

func TestServeConn404(t *testing.T) {
	dir := t.TempDir()
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	errCh := make(chan error, 1)
	go func() {
		err := ServeConn(server, dir, DefaultRoutes(), 2*time.Second)
		_ = server.Close()
		errCh <- err
	}()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprint(client, "GET /does-not-exist HTTP/1.1\r\nHost: x\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(client).ReadString('\n')
	if !strings.Contains(line, "404") {
		t.Fatalf("want 404, got %q", line)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("ServeConn: %v", err)
	}
}

func TestServeConnRejectsOversizedHeaders(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	errCh := make(chan error, 1)
	go func() {
		err := ServeConn(server, t.TempDir(), DefaultRoutes(), time.Second)
		_ = server.Close()
		errCh <- err
	}()
	if _, err := fmt.Fprintf(client, "GET / HTTP/1.1\r\nX-Large: %s\r\n\r\n", strings.Repeat("a", maxHeaderLineBytes+1)); err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(client).ReadString('\n')
	if !strings.Contains(line, "431") {
		t.Fatalf("want 431, got %q", line)
	}
	if err := <-errCh; err == nil {
		t.Fatal("expected oversized-header error")
	}
}
