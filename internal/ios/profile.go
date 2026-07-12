// Package ios generates an encrypted-DNS mobileconfig profile and serves it
// plus a short landing page. The primary payload uses DoH; an optional second
// payload provides a DoT fallback.
//
// The Python legacy (5GPN-X/ios-http.py) was inetd-style: one child per
// TCP connect. The Go port keeps the exact same behavior — a single
// ServeConn() call per accepted socket — so systemd's socket unit can drive
// it identically without a Python interpreter on the host.
package ios

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// ProfileParams parameterizes the mobileconfig template.
type ProfileParams struct {
	Domain      string // required — the primary DoH server hostname
	DisplayName string // shown in Settings; defaults to "5GPN Encrypted DNS"
	Identifier  string // reverse-DNS style unique id, defaults to "com.5gpn.dot"
	UUID        string // required (RFC 4122) — persistent per install
	PayloadUUID string // required — nested payload UUID (distinct from UUID)

	// OnDemand, when true, adds an OnDemandRules array (Action: Connect,
	// InterfaceTypeMatch: WiFi / Cellular) to every DNSSettings payload in
	// the profile, per Apple's DNSSettings.OnDemandRulesElement schema
	// (com.apple.dnsSettings.managed). Plan §4 Phase 8 / AC-I5.
	OnDemand bool

	// FallbackDoT, when non-empty, adds a SECOND DNSSettings payload
	// pointing at this fallback DoT server (hostname, or an IP whose cert carries a
	// matching IP SAN, e.g. Cloudflare's "1.1.1.1") so the device still has
	// a working encrypted resolver if the primary VPS-hosted DoH server
	// goes down. Apple's device-management docs explicitly allow more than
	// one DNSSettings payload per profile for exactly this redundancy case.
	// This is the fix for the v0.2.9 iPhone-blackout regression.
	FallbackDoT string
	// FallbackPayloadUUID is required whenever FallbackDoT is set — every
	// payload dict needs its own distinct PayloadUUID.
	FallbackPayloadUUID string
}

// onDemandRulesBlock is shared by the primary and fallback DNSSettings
// dicts below — Apple's OnDemandRulesElement schema wants Action: Connect
// paired with a single InterfaceTypeMatch value per dict, so "WiFi +
// Cellular" is two dicts, not one dict with an array value.
const onDemandRulesBlock = `                <key>OnDemandRules</key>
                <array>
                    <dict>
                        <key>Action</key>
                        <string>Connect</string>
                        <key>InterfaceTypeMatch</key>
                        <string>WiFi</string>
                    </dict>
                    <dict>
                        <key>Action</key>
                        <string>Connect</string>
                        <key>InterfaceTypeMatch</key>
                        <string>Cellular</string>
                    </dict>
                </array>
`

var mobileConfigTemplate = template.Must(template.New("mobileconfig").Funcs(template.FuncMap{
	"xml": func(v string) string {
		var out bytes.Buffer
		_ = xml.EscapeText(&out, []byte(v))
		return out.String()
	},
}).Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>PayloadContent</key>
    <array>
        <dict>
            <key>DNSSettings</key>
            <dict>
                <key>DNSProtocol</key>
                <string>HTTPS</string>
                <key>ServerName</key>
                <string>{{ xml .Domain }}</string>
                <key>ServerURL</key>
                <string>https://{{ xml .Domain }}/dns-query</string>
{{- if .OnDemand }}
` + onDemandRulesBlock + `{{- end }}
            </dict>
            <key>PayloadDescription</key>
            <string>5GPN gateway encrypted DNS profile</string>
            <key>PayloadDisplayName</key>
            <string>{{ xml .DisplayName }}</string>
            <key>PayloadIdentifier</key>
            <string>{{ xml .Identifier }}.payload</string>
            <key>PayloadType</key>
            <string>com.apple.dnsSettings.managed</string>
            <key>PayloadUUID</key>
            <string>{{ xml .PayloadUUID }}</string>
            <key>PayloadVersion</key>
            <integer>1</integer>
        </dict>
{{- if .FallbackDoT }}
        <dict>
            <key>DNSSettings</key>
            <dict>
                <key>DNSProtocol</key>
                <string>TLS</string>
                <key>ServerName</key>
                <string>{{ xml .FallbackDoT }}</string>
            </dict>
            <key>PayloadDescription</key>
            <string>5GPN gateway fallback encrypted DNS profile</string>
            <key>PayloadDisplayName</key>
            <string>{{ xml .DisplayName }} Fallback</string>
            <key>PayloadIdentifier</key>
            <string>{{ xml .Identifier }}.fallback-payload</string>
            <key>PayloadType</key>
            <string>com.apple.dnsSettings.managed</string>
            <key>PayloadUUID</key>
            <string>{{ xml .FallbackPayloadUUID }}</string>
            <key>PayloadVersion</key>
            <integer>1</integer>
        </dict>
{{- end }}
    </array>
    <key>PayloadDisplayName</key>
    <string>{{ xml .DisplayName }}</string>
    <key>PayloadIdentifier</key>
    <string>{{ xml .Identifier }}</string>
    <key>PayloadRemovalDisallowed</key>
    <false/>
    <key>PayloadType</key>
    <string>Configuration</string>
    <key>PayloadUUID</key>
    <string>{{ xml .UUID }}</string>
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
	if p.FallbackDoT != "" && p.FallbackPayloadUUID == "" {
		return nil, errors.New("ios profile: FallbackPayloadUUID required when FallbackDoT is set")
	}
	if p.DisplayName == "" {
		p.DisplayName = "5GPN Encrypted DNS"
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
		displayName = "5GPN Encrypted DNS"
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
<p><a style="display:inline-block;padding:0.75rem 1.25rem;background:#0a84ff;color:#fff;border-radius:0.5rem;text-decoration:none" href="%s">Install encrypted DNS profile</a></p>
</body></html>
`, html.EscapeString(displayName), html.EscapeString(displayName), html.EscapeString(downloadPath)))
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
