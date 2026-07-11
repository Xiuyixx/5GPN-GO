// Command 5gpn is the personal-gateway daemon entrypoint.
//
// M1: parses config, opens SQLite, migrates, mints a setup token when no
// panel user exists, and serves the panel API + embedded SPA. M2 wires
// TG bot, iOS profile, and the systemd orchestrator by default.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"database/sql"

	"github.com/Xiuyixx/5GPN-Go/internal/api"
	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/core"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
	"github.com/Xiuyixx/5GPN-Go/internal/metrics"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/rulesets"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
	"github.com/Xiuyixx/5GPN-Go/internal/tgbot"
	"github.com/Xiuyixx/5GPN-Go/internal/web"
)

// bootStore is the boot-time projection of DB state that core.Assemble
// consumes. It reads the currently-active rule_versions row and delegates
// exit lookups to the DB-backed exit.Store (single source of truth for
// exits since S2).
type bootStore struct {
	db    *sql.DB
	exits xexit.Store
}

func (b *bootStore) ActiveRulesYAML() (string, bool, error) {
	row, err := db.GetActiveRuleVersion(b.db)
	if errors.Is(err, db.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return row.RulesYAML, true, nil
}

func (b *bootStore) ListExits() ([]core.ExitRecord, error) {
	return b.exits.Records(context.Background())
}

var version = "0.2.8"

func main() {
	configPath := flag.String("config", "/etc/5gpn/config.yaml", "path to config.yaml")
	dataDir := flag.String("data", "/var/lib/5gpn", "state directory (SQLite, snapshots, keys)")
	listenAddr := flag.String("listen", "", "override server address (empty = use config)")
	insecure := flag.Bool("insecure", false, "serve HTTP instead of HTTPS (dev only)")
	orchestratorMode := flag.String("orchestrator", "auto",
		"orchestrator: auto | systemd | noop. auto picks systemd on linux hosts with systemctl, noop otherwise. Use noop in e2e / container hosts that do not own /etc/dnsdist etc.")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger, *configPath, *dataDir, *listenAddr, *insecure, *orchestratorMode); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, configPath, dataDir, listenOverride string, insecure bool, orchestratorMode string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	dbHandle, err := db.Open(db.Config{Path: filepath.Join(dataDir, "5gpn.db")})
	if err != nil {
		return err
	}
	defer dbHandle.Close()
	if err := db.Migrate(dbHandle); err != nil {
		return err
	}

	jwtSecret, err := loadOrCreateJWTSecret(filepath.Join(dataDir, "jwt.key"))
	if err != nil {
		return err
	}

	// Panel-managed settings live in SQLite (panel_settings table). Overlay
	// them onto the YAML cfg so the wizard's saved values win over YAML
	// defaults for the rest of this boot — YAML stays authoritative for
	// anything the wizard has not touched.
	settingsStore := settings.New(dbHandle)
	if err := settings.OverlayConfig(context.Background(), settingsStore, cfg); err != nil {
		logger.Warn("settings overlay skipped", "err", err)
	}

	setupToken := ""
	if n, _ := db.CountPanelUsers(dbHandle); n == 0 {
		setupToken = randomHex(32)
		logger.Warn("no panel user found — one-time setup token below")
		fmt.Printf("\n===============================================================\n")
		fmt.Printf("5GPN SETUP TOKEN (valid until first successful bootstrap):\n\n  %s\n\n", setupToken)
		fmt.Printf("POST /api/v1/bootstrap { token, username, password } to claim.\n")
		fmt.Printf("===============================================================\n\n")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Exit store is the DB-backed truth for exits since S2. Migration
	// seeds a single 'direct' row; if the operator's YAML lists additional
	// exits AND this is truly first boot (only the seed row present),
	// import them once. Idempotent on subsequent boots.
	exitStore := xexit.NewStore(dbHandle)
	if err := seedExitsFromYAML(ctx, exitStore, cfg.Exits, logger); err != nil {
		logger.Warn("exit seed skipped", "err", err)
	}

	// Assemble the effective config once at boot so orchestrator + Applier
	// both agree on what "active" looks like across restarts. Assemble is
	// tolerant of missing DB rows (fresh install) so this is safe on
	// first-boot.
	store := &bootStore{db: dbHandle, exits: exitStore}
	effectiveCfg, err := core.Assemble(cfg, store)
	if err != nil {
		return fmt.Errorf("assemble: %w", err)
	}

	// Orchestrator selection.
	// - `--orchestrator=systemd` forces systemd (fails at first Apply if
	//   systemctl is missing).
	// - `--orchestrator=noop` forces the noop driver — every render+reload
	//   becomes a log line, no filesystem writes to /etc/dnsdist etc. Use
	//   in e2e and container hosts that do not own the data-plane files.
	// - `--orchestrator=auto` (default) picks systemd on Linux hosts with
	//   systemctl on PATH, noop otherwise. Matches pre-flag behavior.
	var orch orchestrator.Orchestrator
	var systemd *orchestrator.Systemd
	useSystemd := false
	switch orchestratorMode {
	case "systemd":
		useSystemd = true
	case "noop":
		useSystemd = false
	case "auto", "":
		useSystemd = orchestrator.AvailableOnHost()
	default:
		return fmt.Errorf("--orchestrator: unknown value %q (want auto | systemd | noop)", orchestratorMode)
	}
	if useSystemd {
		systemd = orchestrator.DefaultSystemd(effectiveCfg, logger)
		orch = systemd
		logger.Info("orchestrator: systemd", "mode", orchestratorMode)
	} else {
		orch = &orchestrator.NoOp{Logger: logger}
		logger.Info("orchestrator: no-op", "mode", orchestratorMode)
	}

	applier := &core.Applier{
		DB:         dbHandle,
		BaseConfig: cfg,
		Store:      store,
		ExitStore:  applierExitAdapter{inner: exitStore},
		Orch:       orch,
		Logger:     logger,
	}
	if systemd != nil {
		systemd.HealthObserver = applier.OnHealth
	}

	activeExit := ""
	if len(effectiveCfg.Exits) > 0 {
		activeExit = effectiveCfg.Exits[0].ID
	}
	logger.Info("core: effective config assembled",
		"effective_rules", len(effectiveCfg.EffectiveRules),
		"exits", len(effectiveCfg.Exits),
		"active_exit", activeExit)

	// Metrics collector: samples every 10s into SQLite metrics_snapshot,
	// trimmed to 24h. Runs for the lifetime of the daemon.
	go metrics.New(metrics.Config{
		DB:       dbHandle,
		Interval: 10 * time.Second,
		Logger:   logger,
	}).Run(ctx)

	binPath, _ := os.Executable()

	// TG bot Manager. Owned by the daemon lifecycle; wired into the
	// panel so /api/v1/settings/tgbot can hot-reload the bot without
	// restarting the daemon. Start below; ignore ErrBotDisabled so a
	// fresh install with no token boots cleanly.
	exitState := buildTGBotExitState(exitStore, applier, logger)
	botMgr := tgbot.NewManager(tgbot.ManagerConfig{
		Handlers: &tgbot.DefaultHandlers{
			DB:        dbHandle,
			Logger:    logger,
			ExitState: exitState,
			IOSPort:   cfg.IOS.HTTPPort,
		},
		Logger: logger,
	})

	rulesetsStore := rulesets.New(dbHandle)
	rulesetSyncer := rulesets.NewSyncer(rulesetsStore, logger, rulesets.SyncOptions{})
	// Background refresh: every 6 hours we re-check every enabled ruleset
	// with ETag / If-Modified-Since so the operator's cached rule count
	// stays honest without hitting the network at every apply.
	go rulesetSyncer.Run(ctx, 6*time.Hour)

	srv := api.New(dbHandle, api.Config{
		SessionTTL:     cfg.Panel.SessionTTL,
		LoginPerMinute: cfg.Panel.RateLimit.LoginPerMinute,
		LockoutMinutes: cfg.Panel.RateLimit.LockoutMinutes,
		JWTSecret:      jwtSecret,
		Issuer:         cfg.Server.Domain,
		SetupToken:     setupToken,
		WebFS:          web.FS,
		Orchestrator:   orch,
		BaseConfig:     cfg,
		Applier:        applier,
		Store:          exitStore,
		Updater: api.UpdaterConfig{
			Owner:      "Xiuyixx",
			Repo:       "5GPN-Go",
			Unit:       "5gpn",
			BinaryPath: binPath,
			Version:    version,
		},
		TGBot:         botMgr,
		Settings:      settingsStore,
		Rulesets:      rulesetsStore,
		RulesetSyncer: rulesetSyncer,
	}, logger)

	addr := listenOverride
	if addr == "" {
		addr = fmt.Sprintf("%s:%d", cfg.Server.PanelBind, cfg.Server.PanelPort)
	}

	cert, key := cfg.Server.TLS.Cert, cfg.Server.TLS.Key
	if insecure {
		cert, key = "", ""
	}

	// Auto-TLS via certmagic when the wizard turned it on. The overlay
	// gave us the effective domain; --insecure short-circuits to plain
	// HTTP so dev boots stay simple. cert/key files, when supplied, win
	// over ACME so operators keeping a manual chain (e.g. dnsdist) are
	// not surprised.
	if !insecure && cert == "" && key == "" {
		acmeEnabled, _ := settingsStore.GetBool(context.Background(), settings.KeyTLSACMEEnabled)
		acmeEmail, _ := settingsStore.GetString(context.Background(), settings.KeyTLSACMEEmail)
		if acmeEnabled && cfg.Server.Domain != "" && acmeEmail != "" {
			srv.ACME = api.ACMEOptions{
				Domain:     cfg.Server.Domain,
				Email:      acmeEmail,
				StorageDir: api.ACMEStorageDir(dataDir),
			}
		}
	}

	// Fail-fast guard: refuse to bind plain HTTP on a TLS-standard port.
	// Without this, a half-configured wizard save (port 443, ACME off,
	// no cert) yields a listener that serves HTTP on :443. Browsers
	// send TLS ClientHello, get a torn-down TCP connection back and
	// surface ERR_SSL_PROTOCOL_ERROR — a very confusing failure mode.
	// Refusing here forces the operator to either finish the ACME
	// wizard step or move the panel off 80/443. Mirror this list in
	// web/src/pages/Wizard.tsx (TLS_STANDARD_PORTS).
	if !insecure && cert == "" && key == "" && srv.ACME.Domain == "" {
		switch cfg.Server.PanelPort {
		case 80, 443:
			return fmt.Errorf(
				"panel_port %d is a TLS-standard port but no TLS is configured "+
					"(no static cert, no ACME). Re-run the wizard with Auto-SSL enabled, "+
					"or move server.panel_port off %d. Booting plain HTTP on this port "+
					"would cause ERR_SSL_PROTOCOL_ERROR for every browser visit",
				cfg.Server.PanelPort, cfg.Server.PanelPort)
		}
	}
	// Boot the bot from config. Empty token disables it cleanly (fresh
	// install path); non-empty starts the Serve loop under the daemon ctx.
	if err := botMgr.Start(ctx, cfg.TGBot.Token, cfg.TGBot.AdminChatIDs); err != nil {
		if errors.Is(err, tgbot.ErrBotDisabled) {
			logger.Info("tgbot disabled at boot (no token) — panel can enable later")
		} else {
			logger.Warn("tgbot start failed — panel still serves", "err", err)
		}
	}

	// ctl socket: Linux-only privileged CLI channel (SO_PEERCRED gated).
	// On non-Linux the stub is a no-op so main.go compiles cross-platform.
	go startCtlSocket(ctx, applier, exitStore, dbHandle, logger)

	// iOS profile server: honors systemd socket activation (LISTEN_FDS) if
	// present, otherwise binds cfg.IOS.HTTPPort directly. Skipped when the
	// port is zero and no activated sockets exist.
	go func() {
		wwwDir := filepath.Join(dataDir, "ios")
		if err := runIOSListener(ctx, cfg.IOS.HTTPPort, wwwDir, logger); err != nil {
			logger.Warn("ios listener stopped", "err", err)
		}
	}()

	logger.Info("5gpn daemon starting",
		"version", version, "addr", addr, "data", dataDir, "insecure", insecure)
	return srv.ListenAndServe(ctx, addr, cert, key)
}

func loadOrCreateJWTSecret(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		return raw, nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("jwt secret read: %w", err)
	}
	buf := make([]byte, 64)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return nil, fmt.Errorf("jwt secret write: %w", err)
	}
	return buf, nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// seedExitsFromYAML runs once at boot: if the DB currently holds only the
// migration seed row ('direct') AND the operator's YAML lists additional
// exits, import them. On subsequent boots the DB will have more rows so
// this is a no-op — the DB is now the single source of truth for exits.
func seedExitsFromYAML(ctx context.Context, store xexit.Store, yamlExits []config.ExitConfig, logger *slog.Logger) error {
	if len(yamlExits) == 0 {
		return nil
	}
	existing, err := store.List(ctx)
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	if !(len(existing) == 1 && existing[0].ExitID == "direct") {
		return nil
	}
	for _, ex := range yamlExits {
		if ex.ID == "direct" {
			continue
		}
		uri, _ := ex.Config["uri"].(string)
		if uri == "" {
			logger.Warn("exit YAML seed: no config.uri present — skipping", "id", ex.ID)
			continue
		}
		if _, err := store.Add(ctx, ex.ID, uri); err != nil {
			logger.Warn("exit YAML seed: add failed", "id", ex.ID, "err", err)
			continue
		}
		logger.Info("exit YAML seeded into DB", "id", ex.ID)
	}
	return nil
}

// applierExitAdapter narrows xexit.Store down to the core.ExitStore
// contract (Active returns exit_id string, not the full Exit struct).
// Keeps internal/core free of an internal/exit import cycle.
type applierExitAdapter struct{ inner xexit.Store }

func (a applierExitAdapter) Active(ctx context.Context) (string, error) {
	e, err := a.inner.Active(ctx)
	if err != nil {
		if errors.Is(err, xexit.ErrNoActive) {
			return "", nil
		}
		return "", err
	}
	return e.ExitID, nil
}

func (a applierExitAdapter) Switch(ctx context.Context, exitID string) error {
	return a.inner.Switch(ctx, exitID)
}

// buildTGBotExitState wires the tgbot function-pointer seam onto the
// DB-backed exit.Store + core.Applier. Add/Delete hit the store directly
// (no data-plane render). Switch goes through Applier.SwitchExit so the
// bot and panel share the same apply_status / rollback machinery.
func buildTGBotExitState(store xexit.Store, applier *core.Applier, logger *slog.Logger) *tgbot.ExitState {
	return &tgbot.ExitState{
		Items: func() []tgbot.Exit {
			items, err := store.List(context.Background())
			if err != nil {
				logger.Warn("tgbot ExitState.Items", "err", err)
				return nil
			}
			out := make([]tgbot.Exit, 0, len(items))
			for _, e := range items {
				host, _ := e.ProxyConfig["server"].(string)
				port, _ := e.ProxyConfig["port"].(int)
				out = append(out, tgbot.Exit{
					ID:       e.ExitID,
					Protocol: e.Protocol,
					Server:   host,
					Port:     port,
					Active:   e.Active,
				})
			}
			return out
		},
		Switch: func(id string) error {
			_, err := applier.SwitchExit(context.Background(), id)
			return err
		},
		Add: func(id, uri string) error {
			_, err := store.Add(context.Background(), id, uri)
			return err
		},
		Delete: func(id string) error {
			return store.Delete(context.Background(), id)
		},
		Active: func() string {
			e, err := store.Active(context.Background())
			if err != nil {
				return ""
			}
			return e.ExitID
		},
	}
}
