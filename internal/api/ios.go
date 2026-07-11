// iOS profile endpoints — served over the panel's HTTPS listener so the
// Let's Encrypt cert covers them. Historically we ran a separate plain-
// HTTP listener on :8111, but Apple refuses to install .mobileconfig
// files over HTTP OTA, and the split listener needed its own cert story
// that never quite landed. Moving both endpoints onto the panel router
// keeps everything under the ACME-managed :443 cert.

package api

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/Xiuyixx/5GPN-Go/internal/ios"
)

// handleIOSProfileURL returns the https URL where the iOS mobileconfig
// profile can be downloaded. Uses the panel domain on standard :443 so
// the LE cert applies — no port suffix, no separate listener.
func (s *Server) handleIOSProfileURL(w http.ResponseWriter, r *http.Request) {
	if s.BaseConfig == nil {
		writeError(w, http.StatusInternalServerError, "no_config", "server has no base config")
		return
	}
	domain := s.BaseConfig.Server.Domain
	if domain == "" {
		writeError(w, http.StatusInternalServerError, "no_domain", "server.domain is not configured")
		return
	}
	url := fmt.Sprintf("https://%s/ios-dot.mobileconfig", domain)
	writeJSON(w, http.StatusOK, map[string]any{
		"url":  url,
		"uuid": iosProfileUUID(domain, s.BaseConfig.IOS.ProfileUUID),
	})
}

// handleIOSMobileconfig renders and serves the DoT profile on demand.
// Public + unauthenticated: Apple's OTA install flow hits this URL
// without any bearer token, and everything it discloses (the DoT server
// hostname) is already public via DNS.
func (s *Server) handleIOSMobileconfig(w http.ResponseWriter, r *http.Request) {
	if s.BaseConfig == nil {
		writeError(w, http.StatusInternalServerError, "no_config", "server has no base config")
		return
	}
	domain := s.BaseConfig.Server.Domain
	if domain == "" {
		writeError(w, http.StatusInternalServerError, "no_domain", "server.domain is not configured")
		return
	}
	profileUUID := iosProfileUUID(domain, s.BaseConfig.IOS.ProfileUUID)
	payload, err := ios.Render(ios.ProfileParams{
		Domain:      domain,
		UUID:        profileUUID,
		PayloadUUID: iosPayloadUUID(profileUUID),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "render", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-apple-aspen-config")
	w.Header().Set("Content-Disposition", `attachment; filename="ios-dot.mobileconfig"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(payload)
}

// iosProfileUUID resolves the ProfileUUID for the mobileconfig. The
// config file typically ships with "auto", meaning: derive a stable v5
// UUID from the panel domain. Explicit UUIDs from the operator win.
// The result is stable across boots so an already-installed profile on
// a device stays recognized as "the same profile" rather than a
// duplicate on reinstall.
func iosProfileUUID(domain, configured string) string {
	if configured != "" && configured != "auto" {
		if _, err := uuid.Parse(configured); err == nil {
			return configured
		}
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("5gpn-dot-profile:"+domain)).String()
}

// iosPayloadUUID derives a distinct UUID for the nested payload from
// the outer ProfileUUID. Apple requires the two to differ; deriving
// keeps them stable and eliminates persisting a second value.
func iosPayloadUUID(profileUUID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("5gpn-dot-payload:"+profileUUID)).String()
}
