package api

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"

	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

func TestBootstrapClaimIsAtomic(t *testing.T) {
	srv := testServer(t)
	requests := make([]*http.Request, 2)
	for i := range requests {
		body := `{"token":"setup-token-for-tests","username":"admin` + string(rune('a'+i)) + `","password":"supersecret1"}`
		requests[i] = httptest.NewRequest(http.MethodPost, "/api/v1/bootstrap", strings.NewReader(body))
		requests[i].Header.Set("Content-Type", "application/json")
	}
	statuses := make([]int, len(requests))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			rr := httptest.NewRecorder()
			srv.Router().ServeHTTP(rr, requests[i])
			statuses[i] = rr.Code
		}(i)
	}
	close(start)
	wg.Wait()
	created := 0
	for _, status := range statuses {
		if status == http.StatusCreated {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created=%d statuses=%v, want exactly one bootstrap winner", created, statuses)
	}
	count, err := db.CountPanelUsers(srv.DB)
	if err != nil || count != 1 {
		t.Fatalf("panel users=%d err=%v, want exactly one", count, err)
	}
}

func TestEventQueryTokenIsRejected(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	rr := do(t, srv, jsonReq(t, http.MethodGet, "/api/v1/events/logs?unit=5gpn&access_token="+token, nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("query token status=%d, want 401", rr.Code)
	}
}

func TestLogUnitAllowlistRejectsOtherServices(t *testing.T) {
	srv, token := bootstrapAndLogin(t, Config{})
	rr := authGet(t, srv, "/api/v1/events/logs?unit=ssh.service", token)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400: %s", rr.Code, rr.Body.String())
	}
}

func TestClientIPIgnoresForwardedHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/login", nil)
	r.RemoteAddr = "127.0.0.1:4321"
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	if got := clientIP(r); got != "127.0.0.1" {
		t.Fatalf("clientIP=%q, want transport peer", got)
	}
}

func TestIPLimiterPrunesIdleVisitors(t *testing.T) {
	l := newIPLimiter(1, 1, 15)
	now := time.Now()
	l.visitors["stale"] = &ipRecord{
		lim:      rate.NewLimiter(1, 1),
		lastSeen: now.Add(-limiterVisitorTTL - time.Minute),
	}
	l.lastSweep = now.Add(-limiterSweepEvery - time.Minute)
	if ok, reason := l.allow("fresh"); !ok {
		t.Fatalf("fresh visitor rejected: %s", reason)
	}
	if _, exists := l.visitors["stale"]; exists {
		t.Fatal("idle visitor was not pruned")
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := testServer(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	r.TLS = &tls.ConnectionState{}
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, r)
	for _, header := range []string{
		"Content-Security-Policy", "Strict-Transport-Security",
		"X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy",
	} {
		if rr.Header().Get(header) == "" {
			t.Errorf("missing %s", header)
		}
	}
}

func TestBootstrapStatusRejectsMalformedWizardSetting(t *testing.T) {
	srv, _ := bootstrapAndLogin(t, Config{})
	if err := srv.Settings.Set(t.Context(), settings.KeyWizardComplete, `"not-a-bool"`, "test"); err != nil {
		t.Fatal(err)
	}
	rr := do(t, srv, jsonReq(t, http.MethodGet, "/api/v1/bootstrap", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500: %s", rr.Code, rr.Body.String())
	}
	var body APIError
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error != "settings_error" {
		t.Fatalf("error=%q, want settings_error", body.Error)
	}
}
