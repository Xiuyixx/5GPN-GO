// Package ios generates the DoT mobileconfig profile and serves it plus a
// short landing HTML over the systemd-socket-activated port 8111 handler.
//
// The Python legacy (5GPN-X/ios-http.py) was inetd-style: one child per
// TCP connect. The Go port keeps the exact same behavior — a single
// ServeConn() call per accepted socket — so systemd's socket unit can drive
// it identically without a Python interpreter on the host.
package ios

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// ProfileParams parameterizes the mobileconfig template.
type ProfileParams struct {
	Domain      string // required — the DoT server hostname
	DisplayName string // shown in Settings; defaults to "5GPN DoT"
	Identifier  string // reverse-DNS style unique id, defaults to "com.5gpn.dot"
	UUID        string // required (RFC 4122) — persistent per install
	PayloadUUID string // required — nested payload UUID (distinct from UUID)
}

var mobileConfigTemplate = template.Must(template.New("mobileconfig").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>PayloadContent</key>
    <array>
        <dict>
            <key>DNSSettings</key>
            <dict>
                <key>DNSProtocol</key>
                <string>TLS</string>
                <key>ServerName</key>
                <string>{{ .Domain }}</string>
            </dict>
            <key>PayloadDescription</key>
            <string>5GPN gateway encrypted DNS profile</string>
            <key>PayloadDisplayName</key>
            <string>{{ .DisplayName }}</string>
            <key>PayloadIdentifier</key>
            <string>{{ .Identifier }}.payload</string>
            <key>PayloadType</key>
            <string>com.apple.dnsSettings.managed</string>
            <key>PayloadUUID</key>
            <string>{{ .PayloadUUID }}</string>
            <key>PayloadVersion</key>
            <integer>1</integer>
        </dict>
    </array>
    <key>PayloadDisplayName</key>
    <string>{{ .DisplayName }}</string>
    <key>PayloadIdentifier</key>
    <string>{{ .Identifier }}</string>
    <key>PayloadRemovalDisallowed</key>
    <false/>
    <key>PayloadType</key>
    <string>Configuration</string>
    <key>PayloadUUID</key>
    <string>{{ .UUID }}</string>
    <key>PayloadVersion</key>
    <integer>1</integer>
</dict>
</plist>
`))

// Render writes the profile document for the given params.
func Render(p ProfileParams) ([]byte, error) {
	if strings.TrimSpace(p.Domain) == "" {
		return nil, errors.New("ios profile: Domain required")
	}
	if p.UUID == "" || p.PayloadUUID == "" {
		return nil, errors.New("ios profile: UUID + PayloadUUID required")
	}
	if p.DisplayName == "" {
		p.DisplayName = "5GPN DoT"
	}
	if p.Identifier == "" {
		p.Identifier = "com.5gpn.dot"
	}
	var buf bytes.Buffer
	if err := mobileConfigTemplate.Execute(&buf, p); err != nil {
		return nil, fmt.Errorf("ios profile: render: %w", err)
	}
	return buf.Bytes(), nil
}

// LandingHTML is a minimal one-tap install page pointing at
// /ios-dot.mobileconfig. Matches the shape of the legacy www dir.
func LandingHTML(displayName, downloadPath string) []byte {
	if displayName == "" {
		displayName = "5GPN DoT"
	}
	if downloadPath == "" {
		downloadPath = "/ios-dot.mobileconfig"
	}
	return []byte(fmt.Sprintf(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>%s</title>
<meta name="viewport" content="width=device-width,initial-scale=1"></head>
<body style="font-family:-apple-system,BlinkMacSystemFont,sans-serif;padding:2rem;max-width:32rem;margin:auto;">
<h1>%s</h1>
<p>Tap the button below on your iPhone or iPad, then approve the profile in Settings.</p>
<p><a style="display:inline-block;padding:0.75rem 1.25rem;background:#0a84ff;color:#fff;border-radius:0.5rem;text-decoration:none" href="%s">Install DoT profile</a></p>
</body></html>
`, displayName, displayName, downloadPath))
}

// WriteAsset is a small helper for materializing the mobileconfig into a
// data directory (e.g. /var/lib/5gpn/www/) so the socket-activated handler
// can serve it from disk. Directory is created if missing.
func WriteAsset(dir, filename string, body []byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), body, 0o644)
}
