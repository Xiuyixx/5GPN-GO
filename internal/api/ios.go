package api

import (
	"fmt"
	"net/http"
)

// handleIOSProfileURL returns the https URL where the iOS mobileconfig
// profile can be downloaded. Combines the panel domain with the iOS
// listener port from config (default 8111 when unset).
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
	port := s.BaseConfig.IOS.HTTPPort
	if port == 0 {
		port = 8111
	}
	url := fmt.Sprintf("https://%s:%d/ios-dot.mobileconfig", domain, port)
	writeJSON(w, http.StatusOK, map[string]any{
		"url":  url,
		"port": port,
		"uuid": s.BaseConfig.IOS.ProfileUUID,
	})
}
