package api

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
)

// Server owns the HTTP router + auth + rate limiter for the panel API.
type Server struct {
	DB           *sql.DB
	Auth         *Authenticator
	Limiter      *ipLimiter
	Logger       *slog.Logger
	WebFS        fs.FS
	SetupToken   string
	Orchestrator orchestrator.Orchestrator
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
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
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

		r.Get("/api/v1/rules", s.handleListRules)
		r.Post("/api/v1/rules/dry-run", s.handleDryRun)
		r.Post("/api/v1/rules/apply", s.handleApply)

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

// ListenAndServe starts the HTTPS server, or plain HTTP if certFile/keyFile
// are both empty. Blocks until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context, addr, certFile, keyFile string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		var err error
		if certFile != "" && keyFile != "" {
			s.Logger.Info("panel listening HTTPS", "addr", addr)
			err = srv.ListenAndServeTLS(certFile, keyFile)
		} else {
			s.Logger.Info("panel listening HTTP (no TLS)", "addr", addr)
			err = srv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}
