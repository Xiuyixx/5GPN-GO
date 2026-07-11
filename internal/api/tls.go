// TLS + dual-listener wiring.
//
// Three modes, picked at boot from config.yaml + panel_settings overlay:
//
//   1. Insecure  —  plain HTTP on cfg.Server.PanelPort (dev / --insecure).
//   2. Static    —  HTTPS on PanelPort using cfg.Server.TLS.Cert + .Key
//                    (legacy operator-managed certs, e.g. dnsdist chain).
//   3. ACME      —  HTTPS on BOTH cfg.Server.PanelPort AND :443 using a
//                    certmagic-issued Let's Encrypt certificate. certmagic
//                    handles HTTP-01 challenges on :80 in the background
//                    and renews the cert automatically.
//
// The dual listener in mode 3 is what the wizard's "domain" step
// enables: browsers hitting https://<domain>/ hit :443 and don't need
// to remember the panel port, while operators can still reach the
// panel through :8443 (or wherever PanelPort points) over an SSH
// tunnel or a firewalled interface.

package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/caddyserver/certmagic"
)

// ACMEOptions captures the config the daemon needs to switch on
// certmagic. Zero value = disabled (fall back to static TLS or insecure).
type ACMEOptions struct {
	Domain     string // panel FQDN (SAN)
	Email      string // ACME account contact
	StorageDir string // filesystem path certmagic uses for account+cert
	// UseStaging routes issuance through Let's Encrypt's staging CA so
	// e2e tests don't consume the operator's rate limit. Production
	// leaves this false.
	UseStaging bool
}

// listenerConfig is what Server.ListenAndServe unpacks into: the
// primary listener always runs, the acme :443 listener is optional.
type listenerConfig struct {
	primaryAddr string
	tlsConfig   *tls.Config // non-nil = serve HTTPS on primary
	acmeAddr    string      // ":443" when dual mode is active; "" when disabled
}

// setupACME wires certmagic against opts.Domain and returns a
// *tls.Config the http.Server can plug into TLSConfig. Cert management
// runs in a background goroutine; the returned tls.Config picks up
// whichever cert certmagic has cached for opts.Domain via
// GetCertificate.
func setupACME(ctx context.Context, opts ACMEOptions, logger *slog.Logger) (*tls.Config, error) {
	if opts.Domain == "" {
		return nil, errors.New("acme: domain is required")
	}
	if opts.Email == "" {
		return nil, errors.New("acme: contact email is required")
	}
	storage := opts.StorageDir
	if storage == "" {
		return nil, errors.New("acme: storage dir is required")
	}

	// Configure certmagic. Default() gives us a config wired to
	// FileStorage rooted at storage — no ambient state, no globals.
	certmagic.DefaultACME.Agreed = true
	certmagic.DefaultACME.Email = opts.Email
	if opts.UseStaging {
		certmagic.DefaultACME.CA = certmagic.LetsEncryptStagingCA
	} else {
		certmagic.DefaultACME.CA = certmagic.LetsEncryptProductionCA
	}

	magic := certmagic.NewDefault()
	magic.Storage = &certmagic.FileStorage{Path: storage}

	// ManageAsync fires off the initial ACME order in a goroutine so
	// daemon boot doesn't block on a live ACME round-trip. On success
	// the TLSConfig callback below returns the fresh cert. If issuance
	// is still in flight, TLS handshake fails once — the browser
	// retries and by then the cert is available.
	if err := magic.ManageAsync(ctx, []string{opts.Domain}); err != nil {
		return nil, fmt.Errorf("acme: manage %q: %w", opts.Domain, err)
	}

	if logger != nil {
		logger.Info("acme: managing domain", "domain", opts.Domain, "storage", storage, "staging", opts.UseStaging)
	}

	// TLSConfig() returns a tls.Config with the GetCertificate hook
	// pointed at certmagic's in-memory cache. NextProtos includes
	// acme-tls/1 so TLS-ALPN-01 challenges work too (defensive; we
	// primarily use HTTP-01 via certmagic's built-in :80 solver).
	return magic.TLSConfig(), nil
}

// ACMEStorageDir picks the on-disk directory certmagic uses. Placed
// under DataDir so systemd's ProtectSystem=strict + ReadWritePaths=
// DataDir carve-out (installer/templates.go) lets the daemon write.
func ACMEStorageDir(dataDir string) string {
	return filepath.Join(dataDir, "acme")
}

// FrontdoorTLSConfig is the public entry point cmd/5gpn wires the v0.3.0
// front-door listeners against. It's a thin wrapper over setupACME so the
// four DNS listeners (DoT / DoH / DoQ / DoH3) share the same on-disk
// cert cache and renewal cycle as the panel HTTPS listener. Calling it
// after api.Server has already set up ACME is safe: certmagic's default
// registry is idempotent, and the returned tls.Config's GetCertificate
// pulls from the same in-memory cache the panel's TLSConfig uses.
func FrontdoorTLSConfig(ctx context.Context, domain, email, storageDir string, logger *slog.Logger) (*tls.Config, error) {
	return setupACME(ctx, ACMEOptions{
		Domain:     domain,
		Email:      email,
		StorageDir: storageDir,
	}, logger)
}

// runDual starts the primary listener AND, when acmeAddr is non-empty,
// a second listener on that address sharing the same handler + TLS
// config. Blocks until ctx is done or a listener returns an error.
func (s *Server) runDual(ctx context.Context, lc listenerConfig) error {
	handler := s.Router()

	primary := &http.Server{
		Addr:              lc.primaryAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
	}
	if lc.tlsConfig != nil {
		primary.TLSConfig = lc.tlsConfig.Clone()
	}

	var acmeSrv *http.Server
	if lc.acmeAddr != "" && lc.tlsConfig != nil {
		acmeSrv = &http.Server{
			Addr:              lc.acmeAddr,
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      60 * time.Second,
			TLSConfig:         lc.tlsConfig.Clone(),
		}
	}

	errCh := make(chan error, 2)

	go func() {
		var err error
		if primary.TLSConfig != nil {
			s.Logger.Info("panel listening HTTPS", "addr", primary.Addr)
			err = primary.ListenAndServeTLS("", "")
		} else {
			s.Logger.Info("panel listening HTTP (no TLS)", "addr", primary.Addr)
			err = primary.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("primary: %w", err)
			return
		}
		errCh <- nil
	}()

	if acmeSrv != nil {
		go func() {
			s.Logger.Info("panel listening HTTPS (ACME)", "addr", acmeSrv.Addr)
			err := acmeSrv.ListenAndServeTLS("", "")
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("acme_listener: %w", err)
				return
			}
			errCh <- nil
		}()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = primary.Shutdown(shutdownCtx)
		if acmeSrv != nil {
			_ = acmeSrv.Shutdown(shutdownCtx)
		}
		return nil
	case err := <-errCh:
		return err
	}
}
