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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/core"
	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
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
	DB           *sql.DB
	Auth         *Authenticator
	Limiter      *ipLimiter
	Logger       *slog.Logger
	WebFS        fs.FS
	SetupToken   string
	Orchestrator orchestrator.Orchestrator
	BaseConfig   *config.Config
	Applier      *core.Applier
	Store        xexit.Store
	Updater      UpdaterConfig
	TGBot        *tgbot.Manager
	Settings     *settings.Store
	Rulesets      *rulesets.Store
	RulesetSyncer *rulesets.Syncer
	// ACME is optional. When ACME.Domain is non-empty, ListenAndServe
	// starts certmagic + a second HTTPS listener on :443 in addition to
	// the primary panel port.
	ACME ACMEOptions
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
		Limiter:      newIPLimiter(float64(cfg.LoginPerMinute)/60.0, cfg.LoginPerMinute, cfg.LockoutMinutes),
		Logger:       logger,
		WebFS:        cfg.WebFS,
		SetupToken:   cfg.SetupToken,
		Orchestrator: cfg.Orchestrator,
		BaseConfig:   cfg.BaseConfig,
		Applier:      cfg.Applier,
		Store:        cfg.Store,
		Updater:      cfg.Updater,
		TGBot:         cfg.TGBot,
		Settings:      cfg.Settings,
		Rulesets:      cfg.Rulesets,
		RulesetSyncer: cfg.RulesetSyncer,
	}
}

// Router returns a fully wired chi router.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"https://*", "http://localhost:5173"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/api/v1/health", func(w http.ResponseWriter, req *http.Request) {
		// version is exposed here (not just on the authenticated /me path)
		// so the panel's post-upgrade poll can detect the new daemon
		// coming back up before the auth session has been re-established.
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

	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Post("/api/v1/logout", s.handleLogout)
		r.Get("/api/v1/me", s.handleMe)
		r.Post("/api/v1/password", s.handleChangePassword)

		r.Get("/api/v1/rules", s.handleListRules)
		r.Post("/api/v1/rules/dry-run", s.handleDryRun)
		r.Post("/api/v1/rules/apply", s.handleApply)
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

		r.Get("/api/v1/exits", s.handleListExits)
		r.Post("/api/v1/exits/add", s.handleAddExit)
		r.Post("/api/v1/exits/delete", s.handleDeleteExit)
		r.Post("/api/v1/exits/switch", s.handleSwitchExit)

		r.Get("/api/v1/snapshots", s.handleListSnapshots)
		r.Post("/api/v1/snapshots/{id}/rollback", s.handleRollbackSnapshot)

		r.Get("/api/v1/backup/export", s.handleExportBackup)
		r.Post("/api/v1/backup/import", s.handleImportBackup)

		r.Get("/api/v1/metrics", s.handleMetrics)
		r.Get("/api/v1/events/logs", s.handleLogsSSE)

		r.Get("/api/v1/update/check", s.handleUpdateCheck)
		r.Post("/api/v1/update/apply", s.handleUpdateApply)
		r.Post("/api/v1/system/restart", s.handleSystemRestart)

		r.Get("/api/v1/ios/profile-url", s.handleIOSProfileURL)
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
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.Copy(w, f)
}

// ListenAndServe starts the panel listener(s). Three modes:
//
//   1. Plain HTTP  - both certFile/keyFile empty AND s.ACME.Domain empty.
//   2. Static TLS  - certFile/keyFile non-empty. HTTPS on `addr` only.
//   3. ACME TLS    - s.ACME.Domain non-empty. HTTPS on `addr` AND :443,
//                    with certmagic managing the certificate. Same tls
//                    config feeds both listeners so a fresh renewal is
//                    picked up on both ports simultaneously.
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
		if addr != ":443" && !strings.HasSuffix(addr, ":443") {
			lc.acmeAddr = ":443"
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
