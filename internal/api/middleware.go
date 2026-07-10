package api

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type ctxKey int

const (
	ctxUserID ctxKey = iota
	ctxUsername
	ctxSessionID
	ctxJWTID
)

// authMiddleware validates the JWT + backing session.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := extractBearer(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		claims, err := s.Auth.Verify(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
		ctx = context.WithValue(ctx, ctxUsername, claims.Username)
		ctx = context.WithValue(ctx, ctxSessionID, claims.SessionID)
		ctx = context.WithValue(ctx, ctxJWTID, claims.RegisteredClaims.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if h == "" {
		return ""
	}
	parts := strings.SplitN(h, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

// ipLimiter enforces the login rate limit + IP lockout.
type ipLimiter struct {
	mu          sync.Mutex
	rps         float64
	burst       int
	lockMinutes int
	visitors    map[string]*ipRecord
}

type ipRecord struct {
	lim       *rate.Limiter
	failures  int
	lockUntil time.Time
}

func newIPLimiter(rps float64, burst, lockMinutes int) *ipLimiter {
	return &ipLimiter{
		rps: rps, burst: burst, lockMinutes: lockMinutes,
		visitors: map[string]*ipRecord{},
	}
}

func (l *ipLimiter) allow(ip string) (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.visitors[ip]
	if rec == nil {
		rec = &ipRecord{lim: rate.NewLimiter(rate.Limit(l.rps), l.burst)}
		l.visitors[ip] = rec
	}
	if time.Now().Before(rec.lockUntil) {
		return false, "ip locked"
	}
	if !rec.lim.Allow() {
		return false, "rate limit"
	}
	return true, ""
}

func (l *ipLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.visitors[ip]
	if rec == nil {
		rec = &ipRecord{lim: rate.NewLimiter(rate.Limit(l.rps), l.burst)}
		l.visitors[ip] = rec
	}
	rec.failures++
	if rec.failures >= 3 {
		rec.lockUntil = time.Now().Add(time.Duration(l.lockMinutes) * time.Minute)
		rec.failures = 0
	}
}

func (l *ipLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if rec := l.visitors[ip]; rec != nil {
		rec.failures = 0
	}
}

// clientIP extracts an ip from RemoteAddr, honoring X-Forwarded-For only from
// loopback callers (dev proxy).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if host == "127.0.0.1" || host == "::1" {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			return strings.TrimSpace(strings.SplitN(fwd, ",", 2)[0])
		}
	}
	return host
}
