package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

type bootstrapRequest struct {
	Token    string `json:"token"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	n, err := db.CountPanelUsers(s.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"needs_setup": n == 0,
	})
}

func (s *Server) handleBootstrapClaim(w http.ResponseWriter, r *http.Request) {
	var req bootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if s.SetupToken == "" || req.Token != s.SetupToken {
		writeError(w, http.StatusForbidden, "invalid_setup_token", "setup token is missing or wrong")
		return
	}
	n, err := db.CountPanelUsers(s.DB)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if n > 0 {
		writeError(w, http.StatusConflict, "already_setup", "bootstrap can only be claimed once")
		return
	}
	if len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "weak_password", "password must be at least 8 characters")
		return
	}
	hash, err := HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", err.Error())
		return
	}
	uid, err := db.InsertPanelUser(s.DB, req.Username, hash)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "insert_error", err.Error())
		return
	}
	// Setup token is one-shot.
	s.SetupToken = ""
	writeJSON(w, http.StatusCreated, map[string]any{"user_id": uid, "username": req.Username})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if ok, reason := s.Limiter.allow(ip); !ok {
		writeError(w, http.StatusTooManyRequests, "rate_limited", reason)
		return
	}

	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.Limiter.recordFailure(ip)
		writeError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	u, err := db.GetPanelUserByUsername(s.DB, req.Username)
	if errors.Is(err, db.ErrNoRows) {
		s.Limiter.recordFailure(ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if !CheckPassword(u.BcryptHash, req.Password) {
		s.Limiter.recordFailure(ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "")
		return
	}
	tok, _, err := s.Auth.Issue(u.ID, u.Username, ip, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "issue_error", err.Error())
		return
	}
	_ = db.TouchLastLogin(s.DB, u.ID)
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: u.Username, Action: "panel.login", Result: "ok", IP: ip,
	})
	s.Limiter.reset(ip)
	writeJSON(w, http.StatusOK, loginResponse{Token: tok})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	sid, _ := r.Context().Value(ctxSessionID).(string)
	if sid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized", "")
		return
	}
	if err := s.Auth.Revoke(sid); err != nil {
		writeError(w, http.StatusInternalServerError, "revoke_error", err.Error())
		return
	}
	actor, _ := r.Context().Value(ctxUsername).(string)
	_ = db.AppendAudit(s.DB, db.AuditEntry{
		Actor: actor, Action: "panel.logout", Result: "ok", IP: clientIP(r),
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	uid, _ := r.Context().Value(ctxUserID).(int64)
	username, _ := r.Context().Value(ctxUsername).(string)
	writeJSON(w, http.StatusOK, map[string]any{"user_id": uid, "username": username})
}
