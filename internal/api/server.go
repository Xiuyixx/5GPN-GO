package api

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/Xiuyixx/5GPN-Go/internal/access"
	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/core"
	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rulesets"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
	"github.com/Xiuyixx/5GPN-Go/internal/tgbot"
	"github.com/Xiuyixx/5GPN-Go/internal/updater"
)

// serverExitAdapter narrows xexit.Store to the core.ExitStore contract
// (Active returns exit_id string, not the Exit struct). Same shape as
// cmd/5gpn/main.go's applierExitAdapter; kept in-package so api.New can
// construct a self-contained Applier when Config.Applier is unset (test
// wiring path).
type serverExitAdapter struct{ inner xexit.Store }

func (a serverExitAdapter) Active(ctx context.Context) (string, error) {
	e, err := a.inner.Active(ctx)
	if err != nil {
		if errors.Is(err, xexit.ErrNoActive) {
			return "", nil
		}
		return "", err
	}
	return e.ExitID, nil
}

func (a serverExitAdapter) Switch(ctx context.Context, exitID string) error {
	return a.inner.Switch(ctx, exitID)
}

func (a serverExitAdapter) Delete(ctx context.Context, exitID string) error {
	return a.inner.Delete(ctx, exitID)
}

// UpdaterConfig captures the release source + local install target for
// the panel-driven updater endpoints. Zero values disable the endpoints
// (GET /update/check returns "not configured").
type UpdaterConfig struct {
	Owner      string
	Repo       string
	Unit       string
	BinaryPath string
	Version    string
	Client     *updater.Client
}

// Server owns the HTTP router + auth + rate limiter for the panel API.
type Server struct {
	DB            *sql.DB
	Auth          *Authenticator
	Limiter       *ipLimiter
	Logger        *slog.Logger
	WebFS         fs.FS
	SetupToken    string
	setupMu       sync.Mutex
	settingsMu    sync.Mutex
	Orchestrator  orchestrator.Orchestrator
	BaseConfig    *config.Config
	Applier       *core.Applier
	Store         xexit.Store
	Updater       UpdaterConfig
	TGBot         *tgbot.Manager
	Settings      *settings.Store
	Rulesets      *rulesets.Store
	RulesetSyncer *rulesets.Syncer
	// Resolver is the DNS plane's atomic RuleTable holder (internal/resolver.
	// Store). It is optional: a nil Resolver means this Server was built
	// without the DNS front-door wired in (e.g. most existing tests), and
	// every resolver-publish step in the rules-apply / rollback handlers
	// becomes a no-op rather than a nil-pointer panic.
	Resolver *resolver.Store
	// Metrics is the DNS plane's lock-free query counter set
	// (internal/resolver.Metrics), read by GET /api/v1/metrics/dns (see
	// dns_metrics.go). Optional: nil means the DNS front-door isn't wired
	// in, and the endpoint reports zero counters instead of panicking.
	Metrics *resolver.Metrics
	// DNSListeners, when set by cmd/5gpn after Frontdoor starts, returns a
	// live snapshot for the dashboard. Nil means the DNS plane is absent.
	DNSListeners func() DNSListenerStatus
	// LiveResolver is the full DNS resolver instance backing the
	// front-door listeners (nil until cmd/5gpn's startFrontdoor has
	// wired one). The path-B settings handler uses it to hot-apply
	// SpoofPolicy changes without a daemon restart.
	LiveResolver *resolver.Resolver
	// DoHHandler is the RFC 8484 DNS-over-HTTPS handler mounted at
	// /dns-query on the panel router. nil until cmd/5gpn wires one;
	// nil means /dns-query returns 404 (SPA fallback served) rather
	// than answering DNS. Populated by startFrontdoor alongside
	// LiveResolver.
	DoHHandler http.Handler
	// applyStore tracks the async rules-apply / rollback lifecycle (see
	// applies.go) that backs GET /api/v1/applies[/{id}]. Always non-nil.
	applyStore *applyStore
	// ACME is optional. When ACME.Domain is non-empty, ListenAndServe
	// starts certmagic + a second HTTPS listener on :443 in addition to
	// the primary panel port.
	ACME ACMEOptions
	// ACMEListenAddr overrides the default ":443" bind for the ACME
	// secondary listener. main.go sets this to "127.0.0.1:8444" when
	// the sniforward transparent proxy owns public :443 and needs the
	// panel to move out of the way. Empty string keeps the historical
	// default (:443).
	ACMEListenAddr string
	// Gate, when non-nil, filters incoming requests by source IP.
	// Used to implement the "internal-only access" business rule
	// where the panel is restricted to the 5G APN private slice.
	// The middleware is only invoked when Gate.Enabled() reports true,
	// so a disabled gate costs one atomic load per request. See
	// internal/access/gate.go and internal/api/internal_only.go.
	Gate *access.Gate
	// MTG drives the externally-installed 9seconds/mtg systemd
	// service. nil-safe: the mtproxy handlers return 503 with
	// mtg_not_wired when this field is nil, which is the correct
	// state on hosts that never installed 9seconds/mtg.
	MTG MTG
}

// Config bundles user-adjustable server knobs.
type Config struct {
	SessionTTL     time.Duration
	LoginPerMinute int
	LockoutMinutes int
	JWTSecret      []byte
	Issuer         string
	SetupToken     string
	WebFS          fs.FS
	Orchestrator   orchestrator.Orchestrator
	BaseConfig     *config.Config
	Applier        *core.Applier
	Store          xexit.Store
	Updater        UpdaterConfig
	TGBot          *tgbot.Manager
	Settings       *settings.Store
	Rulesets       *rulesets.Store
	RulesetSyncer  *rulesets.Syncer
	// Resolver is optional; see Server.Resolver doc comment.
	Resolver *resolver.Store
	// Metrics is optional; see Server.Metrics doc comment.
	Metrics *resolver.Metrics
	// Gate is optional. When wired, the internal-only middleware and
	// the /api/v1/settings/frontdoor/internal-only handler both use
	// the same instance so Refresh() calls from POST take effect
	// live in the middleware without a daemon restart.
	Gate *access.Gate
	// MTG is optional. When nil the mtproxy handlers return 503
	// mtg_not_wired; that's the desired state on hosts that don't
	// run 9seconds/mtg. Populated in cmd/5gpn/main.go with an
	// mtgctl.Controller.
	MTG MTG
}

// New builds a Server from its dependencies.
func New(db *sql.DB, cfg Config, logger *slog.Logger) *Server {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 24 * time.Hour
	}
	if cfg.LoginPerMinute <= 0 {
		cfg.LoginPerMinute = 5
	}
	if cfg.LockoutMinutes <= 0 {
		cfg.LockoutMinutes = 15
	}
	if cfg.Issuer == "" {
		cfg.Issuer = "5gpn"
	}
	if cfg.Orchestrator == nil {
		cfg.Orchestrator = &orchestrator.NoOp{Logger: logger}
	}
	if cfg.BaseConfig == nil {
		cfg.BaseConfig = &config.Config{}
	}
	if cfg.Store == nil {
		cfg.Store = xexit.NewStore(db)
	}
	if cfg.Applier == nil {
		cfg.Applier = &core.Applier{
			DB:         db,
			BaseConfig: cfg.BaseConfig,
			Store:      core.NoStore{},
			ExitStore:  serverExitAdapter{inner: cfg.Store},
			Orch:       cfg.Orchestrator,
			Logger:     logger,
		}
	}
	// The Applier owns the DB + DNS commit boundary. Keep the explicitly
	// supplied Applier (production wiring) aligned with this Server's resolver.
	cfg.Applier.Resolver = cfg.Resolver
	if systemd, ok := cfg.Applier.Orch.(*orchestrator.Systemd); ok {
		systemd.HealthObserver = cfg.Applier.OnHealth
	}
	if cfg.Updater.Client == nil && cfg.Updater.Owner != "" && cfg.Updater.Repo != "" {
		cfg.Updater.Client = updater.New(updater.Config{
			Owner: cfg.Updater.Owner,
			Repo:  cfg.Updater.Repo,
		})
	}
	if cfg.Settings == nil {
		cfg.Settings = settings.New(db)
	}
	return &Server{
		DB: db,
		Auth: &Authenticator{
			DB: db, Secret: cfg.JWTSecret,
			TokenTTL: cfg.SessionTTL, Issuer: cfg.Issuer,
		},
		Limiter:       newIPLimiter(float64(cfg.LoginPerMinute)/60.0, cfg.LoginPerMinute, cfg.LockoutMinutes),
		Logger:        logger,
		WebFS:         cfg.WebFS,
		SetupToken:    cfg.SetupToken,
		Orchestrator:  cfg.Orchestrator,
		BaseConfig:    cfg.BaseConfig,
		Applier:       cfg.Applier,
		Store:         cfg.Store,
		Updater:       cfg.Updater,
		TGBot:         cfg.TGBot,
		Settings:      cfg.Settings,
		Rulesets:      cfg.Rulesets,
		RulesetSyncer: cfg.RulesetSyncer,
		Resolver:      cfg.Resolver,
		Metrics:       cfg.Metrics,
		Gate:          cfg.Gate,
		MTG:           cfg.MTG,
		applyStore:    newApplyStore(),
	}
}

// Router returns a fully wired chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(securityHeaders)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"http://localhost:5173", "http://127.0.0.1:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
	// Internal-only access gate. Ordered AFTER cors so browser
	// preflight OPTIONS from disallowed IPs still get a proper CORS
	// response and the browser surfaces "internal_only_access" 403
	// instead of a generic CORS error. The middleware itself no-ops
	// when the gate is nil or disabled and skips gating the four
	// public-by-design endpoints (health probe from any monitor,
	// bootstrap claim before first login, iOS OTA mobileconfig pull,
	// public DoH /dns-query) — everything else is restricted to
	// clients whose source IP is on the allowlist.
	r.Use(s.internalOnlyMiddleware)

	r.Get("/api/v1/health", func(w http.ResponseWriter, req *http.Request) {
		// Version remains public so health monitors and an external updater
		// can identify the running daemon without an authenticated session.
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      true,
			"version": s.Updater.Version,
		})
	})

	r.Route("/api/v1/bootstrap", func(r chi.Router) {
		r.Get("/", s.handleBootstrapStatus)
		r.Post("/", s.handleBootstrapClaim)
	})

	r.Post("/api/v1/login", s.handleLogin)

	// iOS mobileconfig is served unauthenticated over HTTPS so Apple's OTA
	// install flow can pull it without a bearer token. Content is public
	// anyway (only the encrypted-DNS server hostname). Must be registered BEFORE the
	// SPA catch-all below or the router serves index.html instead.
	r.Get("/ios-dot.mobileconfig", s.handleIOSMobileconfig)

	// RFC 8484 DNS-over-HTTPS. Public + unauthenticated: this is what
	// iOS profiles + DoH clients hit. Mounted here alongside the
	// mobileconfig endpoint so both share the same panel router / TLS
	// listener. Nil DoHHandler means /dns-query falls through to the
	// SPA — which was the v0.3.x bug: iOS clients got the panel HTML
	// as a "DNS response", silently. Now returns 503 with a clear
	// message so the failure mode is obvious.
	if s.DoHHandler != nil {
		r.Method("GET", "/dns-query", s.DoHHandler)
		r.Method("POST", "/dns-query", s.DoHHandler)
	} else {
		r.Handle("/dns-query", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusServiceUnavailable, "doh_not_wired",
				"DoH handler not wired at daemon boot — check that startFrontdoor ran")
		}))
	}

	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Post("/api/v1/logout", s.handleLogout)
		r.Get("/api/v1/me", s.handleMe)
		r.Post("/api/v1/password", s.handleChangePassword)

		r.Get("/api/v1/rules", s.handleListRules)
		r.Post("/api/v1/rules/dry-run", s.handleDryRun)
		r.Post("/api/v1/rules/apply", s.handleApply)
		r.Post("/api/v1/rules/apply/preview", s.handleApplyPreview)
		r.Post("/api/v1/rules/import", s.handleImportRules)
		r.Post("/api/v1/rules/chinalist/sync", s.handleChinalistSync)
		r.Get("/api/v1/rulesets", s.handleListRulesets)
		r.Post("/api/v1/rulesets", s.handleRegisterRuleset)
		r.Post("/api/v1/rulesets/{name}/sync", s.handleSyncRuleset)
		r.Post("/api/v1/rulesets/{name}/enabled", s.handleToggleRuleset)
		r.Delete("/api/v1/rulesets/{name}", s.handleDeleteRuleset)
		r.Get("/api/v1/settings/tgbot", s.handleGetTgbotSettings)
		r.Post("/api/v1/settings/tgbot", s.handleUpdateTgbotSettings)
		r.Get("/api/v1/settings/panel", s.handleGetPanelSettings)
		r.Post("/api/v1/settings/panel", s.handleUpdatePanelSettings)
		r.Get("/api/v1/apply/status", s.handleApplyStatus)
		r.Get("/api/v1/applies/{id}", s.handleApplyGet)
		r.Get("/api/v1/applies", s.handleAppliesList)

		r.Get("/api/v1/exits", s.handleListExits)
		r.Post("/api/v1/exits/add", s.handleAddExit)
		r.Post("/api/v1/exits/delete", s.handleDeleteExit)
		r.Post("/api/v1/exits/switch", s.handleSwitchExit)

		r.Get("/api/v1/snapshots", s.handleListSnapshots)
		r.Post("/api/v1/snapshots/{id}/rollback", s.handleRollbackSnapshot)

		r.Get("/api/v1/backup/export", s.handleExportBackup)
		r.Post("/api/v1/backup/import", s.handleImportBackup)

		r.Get("/api/v1/metrics", s.handleMetrics)
		r.Get("/api/v1/metrics/dns", s.handleDNSMetrics)
		r.Get("/api/v1/events/logs", s.handleLogsSSE)

		r.Get("/api/v1/update/check", s.handleUpdateCheck)
		r.Post("/api/v1/update/apply", s.handleUpdateApply)
		r.Post("/api/v1/system/restart", s.handleSystemRestart)

		r.Get("/api/v1/ios/profile-url", s.handleIOSProfileURL)
		r.Post("/api/v1/settings/ios/preflight", s.handleIOSPreflight)
		r.Post("/api/v1/settings/ios/profile-enabled", s.handleIOSProfileToggle)

		r.Get("/api/v1/settings/frontdoor/proxy", s.handleGetFrontdoorProxy)
		r.Post("/api/v1/settings/frontdoor/proxy", s.handleUpdateFrontdoorProxy)
		r.Post("/api/v1/settings/frontdoor/proxy/preflight", s.handleFrontdoorProxyPreflight)

		r.Get("/api/v1/settings/frontdoor/internal-only", s.handleGetInternalOnly)
		r.Post("/api/v1/settings/frontdoor/internal-only", s.handleUpdateInternalOnly)

		r.Get("/api/v1/settings/mtproxy", s.handleGetMTProxySettings)
		r.Post("/api/v1/settings/mtproxy", s.handleUpdateMTProxySettings)
		r.Post("/api/v1/settings/mtproxy/generate-secret", s.handleGenerateMTProxySecret)
	})

	// Static SPA fallback: any GET that isn't /api/* serves the panel bundle,
	// falling back to index.html so react-router owns client-side routing.
	if s.WebFS != nil {
		fileServer := http.FileServer(http.FS(s.WebFS))
		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			path := req.URL.Path
			if path == "" || path == "/" {
				s.serveIndex(w)
				return
			}
			if _, err := fs.Stat(s.WebFS, path[1:]); err == nil {
				fileServer.ServeHTTP(w, req)
				return
			}
			s.serveIndex(w)
		})
	}

	return r
}

func (s *Server) serveIndex(w http.ResponseWriter) {
	f, err := s.WebFS.Open("index.html")
	if err != nil {
		http.Error(w, "index.html missing from embedded bundle", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, f)
}

// ListenAndServe starts the panel listener(s). Three modes:
//
//  1. Plain HTTP  - both certFile/keyFile empty AND s.ACME.Domain empty.
//  2. Static TLS  - certFile/keyFile non-empty. HTTPS on `addr` only.
//  3. ACME TLS    - s.ACME.Domain non-empty. HTTPS on `addr` AND :443,
//     with certmagic managing the certificate. Same tls
//     config feeds both listeners so a fresh renewal is
//     picked up on both ports simultaneously.
//
// Blocks until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context, addr, certFile, keyFile string) error {
	lc := listenerConfig{primaryAddr: addr}

	switch {
	case s.ACME.Domain != "":
		tlsCfg, err := setupACME(ctx, s.ACME, s.Logger)
		if err != nil {
			return fmt.Errorf("acme setup: %w", err)
		}
		lc.tlsConfig = tlsCfg
		// Dual listener: primary on configured panel port, secondary on
		// standard :443 so browsers can hit https://<domain>/ without
		// remembering the panel port. Skip the :443 listener when the
		// operator already put the panel on :443 to avoid EADDRINUSE.
		//
		// s.ACMEListenAddr overrides the default ":443" — set by
		// main.go when the sniforward transparent proxy is enabled, so
		// the panel binds a loopback port (e.g. 127.0.0.1:8444) that
		// sniforward's SNI-split can hand traffic to. Empty means "use
		// the historical default".
		secondary := ":443"
		if s.ACMEListenAddr != "" {
			secondary = s.ACMEListenAddr
		}
		if addr != secondary && !strings.HasSuffix(addr, secondary) {
			lc.acmeAddr = secondary
		}
	case certFile != "" && keyFile != "":
		cert, err := tlsLoad(certFile, keyFile)
		if err != nil {
			return fmt.Errorf("load tls: %w", err)
		}
		lc.tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
	}

	return s.runDual(ctx, lc)
}

// tlsLoad reads certFile + keyFile into a tls.Certificate. Named so the
// caller ordering is left-to-right consistent with LoadX509KeyPair.
func tlsLoad(certFile, keyFile string) (tls.Certificate, error) {
	return tls.LoadX509KeyPair(certFile, keyFile)
}
