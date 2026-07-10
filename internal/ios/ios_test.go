package ios

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
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
