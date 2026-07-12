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
		ctx = context.WithValue(ctx, ctxJWTID, claims.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractBearer(r *http.Request) string {
	if h := r.Header.Get("Authorization"); h != "" {
		parts := strings.SplitN(h, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			return strings.TrimSpace(parts[1])
		}
	}
	return ""
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		next.ServeHTTP(w, r)
	})
}

// ipLimiter enforces the login rate limit + IP lockout.
type ipLimiter struct {
	mu          sync.Mutex
	rps         float64
	burst       int
	lockMinutes int
	visitors    map[string]*ipRecord
	lastSweep   time.Time
}

type ipRecord struct {
	lim       *rate.Limiter
	failures  int
	lockUntil time.Time
	lastSeen  time.Time
}

const (
	limiterVisitorTTL  = time.Hour
	limiterSweepEvery  = 5 * time.Minute
	limiterMaxVisitors = 10000
)

func newIPLimiter(rps float64, burst, lockMinutes int) *ipLimiter {
	return &ipLimiter{
		rps: rps, burst: burst, lockMinutes: lockMinutes,
		visitors: map[string]*ipRecord{},
	}
}

func (l *ipLimiter) allow(ip string) (bool, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.prune(now)
	rec, ok := l.visitors[ip]
	if !ok {
		if len(l.visitors) >= limiterMaxVisitors && !l.evictOldestUnlocked(now) {
			return false, "rate limiter capacity"
		}
		rec = &ipRecord{lim: rate.NewLimiter(rate.Limit(l.rps), l.burst)}
		l.visitors[ip] = rec
	}
	rec.lastSeen = now
	if now.Before(rec.lockUntil) {
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
		return
	}
	now := time.Now()
	rec.lastSeen = now
	rec.failures++
	if rec.failures >= 3 {
		rec.lockUntil = now.Add(time.Duration(l.lockMinutes) * time.Minute)
		rec.failures = 0
	}
}

func (l *ipLimiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.visitors, ip)
}

func (l *ipLimiter) prune(now time.Time) {
	if !l.lastSweep.IsZero() && now.Sub(l.lastSweep) < limiterSweepEvery {
		return
	}
	l.lastSweep = now
	for ip, rec := range l.visitors {
		if !now.Before(rec.lockUntil) && now.Sub(rec.lastSeen) > limiterVisitorTTL {
			delete(l.visitors, ip)
		}
	}
}

func (l *ipLimiter) evictOldestUnlocked(now time.Time) bool {
	var oldestIP string
	var oldest time.Time
	for ip, rec := range l.visitors {
		if now.Before(rec.lockUntil) {
			continue
		}
		if oldestIP == "" || rec.lastSeen.Before(oldest) {
			oldestIP, oldest = ip, rec.lastSeen
		}
	}
	if oldestIP == "" {
		return false
	}
	delete(l.visitors, oldestIP)
	return true
}

// clientIP uses the transport peer only. The production front door is an L4
// proxy and does not authenticate X-Forwarded-For, so trusting it would let a
// client rotate arbitrary identities and bypass login lockout.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host
}
