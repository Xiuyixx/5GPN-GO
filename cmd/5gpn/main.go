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

	"crypto/tls"
	"database/sql"
	"strings"

	"github.com/Xiuyixx/5GPN-Go/internal/access"
	"github.com/Xiuyixx/5GPN-Go/internal/api"
	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/core"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	xexit "github.com/Xiuyixx/5GPN-Go/internal/exit"
	"github.com/Xiuyixx/5GPN-Go/internal/frontdoor"
	"github.com/Xiuyixx/5GPN-Go/internal/metrics"
	"github.com/Xiuyixx/5GPN-Go/internal/mtgctl"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/proxy/quicforward"
	"github.com/Xiuyixx/5GPN-Go/internal/proxy/sniforward"
	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/rulesets"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
	"github.com/Xiuyixx/5GPN-Go/internal/tgbot"
	"github.com/Xiuyixx/5GPN-Go/internal/web"

	"net"
)

// Default loopback backends used when the operator enables the
// transparent forwarders. The panel HTTPS listener moves to the TCP
// port; DoH3 (if also on) moves to the UDP port; sniforward and
// quicforward own public :443 for their respective transports.
const (
	defaultPanelBackendTCP = "127.0.0.1:8444"
	defaultPanelBackendUDP = "127.0.0.1:8445"
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

var version = "0.3.2"

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

	// Internal-only access gate: shared instance between the panel
	// HTTP middleware and the three proxy accept sites, so a single
	// POST /api/v1/settings/frontdoor/internal-only + Gate.Refresh
	// picks up on every listener without a daemon restart.
	accessGate, err := access.NewGate(settingsStore)
	if err != nil {
		return fmt.Errorf("access gate: %w", err)
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

	// DNS-plane wiring — v0.3.0. resolverStore holds the currently
	// active RuleTable; Publish is called from api.handleApply (and
	// handleRollbackSnapshot) after a snapshot lands, so the DNS
	// listeners see fresh rules without a daemon restart. resolverMetrics
	// is the counter set the panel's Dashboard renders.
	resolverStore := &resolver.Store{}
	resolverMetrics := resolver.NewMetrics()

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
		Resolver:      resolverStore,
		Metrics:       resolverMetrics,
		Gate:          accessGate,
		MTG:           mtgctl.New("", "", "", logger),
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

	// Path-B transparent-forwarder decision: when sniforward is enabled
	// it owns the public TCP :443 socket, so the panel HTTPS listener
	// (whether it's the "primary" bound to panel_port=443 or the ACME
	// secondary on :443) must vacate :443 entirely and bind the
	// loopback panelBackendTCP instead. This must happen BEFORE
	// srv.ListenAndServe so both listeners pick up the move.
	sniForwardOn, _ := settingsStore.GetBool(context.Background(), settings.KeyFrontdoorSNIForwardEnabled)
	panelBackendTCP := defaultPanelBackendTCP
	if v, _ := settingsStore.GetString(context.Background(), settings.KeyFrontdoorPanelBackendTCP); v != "" {
		panelBackendTCP = v
	}
	if sniForwardOn && srv.ACME.Domain != "" {
		srv.ACMEListenAddr = panelBackendTCP
		if strings.HasSuffix(addr, ":443") || strings.HasSuffix(addr, ":443/") {
			logger.Info("path-b: primary panel listener relocated off :443",
				"was", addr, "now", panelBackendTCP)
			addr = panelBackendTCP
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

	// DNS front-door (v0.3.0). Only starts when ACME is active, because
	// DoT/DoH/DoQ/DoH3 all need the LE cert. In --insecure dev boots the
	// front-door is silently skipped — a dev daemon without a real domain
	// has nothing to encrypt with. Plain :53 UDP/TCP is fine as-is when
	// the plan's public_plain_dns_enabled toggle is off (default), because
	// the loopback + WG interface binds don't need any cert.
	if !insecure && srv.ACME.Domain != "" {
		if err := startFrontdoor(ctx, srv, resolverStore, resolverMetrics, settingsStore, dataDir, accessGate, logger); err != nil {
			logger.Warn("frontdoor start failed — panel still serves", "err", err)
		}
	}

	return srv.ListenAndServe(ctx, addr, cert, key)
}

// startFrontdoor wires the v0.3.0 DNS listeners onto the running daemon.
// The plain-DNS bind toggle + DoQ + DoH3 flags live in panel_settings so
// operators can flip them without editing config.yaml; the initial reads
// happen here and Frontdoor.Reconcile takes over for runtime flips.
//
// The four TLS listeners (DoT / DoH / DoQ / DoH3) share the panel's LE
// cert by asking certmagic — the panel already started managing the
// domain via api.setupACME, so this call is idempotent and just returns
// a wrapper over the same on-disk cert. When certmagic later rotates the
// cert both the panel and the front-door pick it up on the next handshake.
//
// Path-B additions (v0.4.x):
//   - When frontdoor.spoof_enabled is on, a SpoofPolicy is published to
//     the resolver so proxy-classified A/AAAA answers point at the
//     gateway IP instead of the real origin.
//   - When frontdoor.sni_forward_enabled is on, a TCP :443 sniforward
//     starts owning public :443 (panel :443 has already been moved to
//     the loopback backend by main). SNI-matched panel traffic is
//     forwarded to that loopback backend; other SNIs are forwarded to
//     the real origin.
//   - When frontdoor.quic_forward_enabled is on, a UDP :443
//     quicforward starts; DoH3 (if also on) is rebound to a loopback
//     UDP port so quicforward can own public :443/UDP.
func startFrontdoor(
	ctx context.Context,
	srv *api.Server,
	store *resolver.Store,
	metrics *resolver.Metrics,
	sset *settings.Store,
	dataDir string,
	accessGate *access.Gate,
	logger *slog.Logger,
) error {
	up := resolver.NewUpstream()
	up.Logger = logger
	res := resolver.NewResolver(store, up, metrics)
	// Expose the live resolver + DoH handler on the api.Server so the
	// panel's /dns-query route actually resolves DNS (v0.3.x had a
	// silent bug where /dns-query returned the SPA HTML because no
	// handler was mounted), and /settings/frontdoor/proxy can
	// hot-apply spoof policy without a restart.
	srv.LiveResolver = res
	srv.DoHHandler = frontdoor.NewDoH(res, logger).Handler()

	// Path-B: publish a SpoofPolicy so proxy-classified A/AAAA answers
	// point at the gateway IP. Read before wiring transports so a
	// misconfiguration surfaces at boot rather than mid-request.
	if err := applySpoofSettings(ctx, res, sset, srv.ACME.Domain, logger); err != nil {
		logger.Warn("frontdoor: spoof configuration skipped", "err", err)
	}

	panelTLS, err := api.FrontdoorTLSConfig(ctx, srv.ACME.Domain, srv.ACME.Email, api.ACMEStorageDir(dataDir), logger)
	if err != nil {
		return fmt.Errorf("frontdoor: certmagic: %w", err)
	}
	getCert := func() (*tls.Certificate, error) {
		return panelTLS.GetCertificate(&tls.ClientHelloInfo{ServerName: srv.ACME.Domain})
	}
	tlsConfigs, err := frontdoor.BuildTLSConfigs(getCert)
	if err != nil {
		return fmt.Errorf("frontdoor: tls configs: %w", err)
	}

	// Public plain :53 stays OFF by default (open-resolver amplification
	// mitigation, plan R13). Operators opt in via panel_settings.
	publicPlain, _ := sset.GetBool(ctx, "frontdoor.public_plain_dns_enabled")
	doqEnabled, _ := sset.GetBool(ctx, settings.KeyFrontdoorDoQEnabled)
	doh3Enabled, _ := sset.GetBool(ctx, settings.KeyFrontdoorDoH3Enabled)
	quicForwardOn, _ := sset.GetBool(ctx, settings.KeyFrontdoorQUICForwardEnabled)

	panelBackendUDP := defaultPanelBackendUDP
	if v, _ := sset.GetString(ctx, settings.KeyFrontdoorPanelBackendUDP); v != "" {
		panelBackendUDP = v
	}

	fdCfg := frontdoor.DefaultConfig()
	fdCfg.PublicPlainDNSEnabled = publicPlain
	fdCfg.DoQEnabled = doqEnabled
	fdCfg.DoH3Enabled = doh3Enabled
	fdCfg.TLSConfigs = tlsConfigs
	if quicForwardOn && doh3Enabled {
		fdCfg.DoH3Bind = []string{panelBackendUDP}
	}

	fd := frontdoor.New(fdCfg, res, logger)
	if err := fd.Start(ctx); err != nil {
		return fmt.Errorf("frontdoor: start: %w", err)
	}
	logger.Info("frontdoor: started",
		"public_plain_dns", publicPlain, "doq", doqEnabled, "doh3", doh3Enabled)

	if err := startProxyForwarders(ctx, sset, srv.ACME.Domain, panelBackendUDP, accessGate, logger); err != nil {
		logger.Warn("frontdoor: proxy forwarders skipped", "err", err)
	}
	return nil
}

// applySpoofSettings publishes a SpoofPolicy on res based on
// panel_settings. Silent no-op if the master switch is off.
func applySpoofSettings(ctx context.Context, res *resolver.Resolver, sset *settings.Store, panelDomain string, logger *slog.Logger) error {
	on, _ := sset.GetBool(ctx, settings.KeyFrontdoorSpoofEnabled)
	if !on {
		return nil
	}
	// server IP: explicit override wins, otherwise use the routing-
	// table's egress source. panelDomain is unused here but kept in
	// the signature so a future implementation could resolve the
	// domain and use its A record as a stability anchor.
	_ = panelDomain
	ipStr, _ := sset.GetString(ctx, settings.KeyFrontdoorSpoofServerIP)
	if ipStr == "" {
		ipStr = discoverEgressIP()
	}
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return fmt.Errorf("spoof: server_ip not set and autodetect failed")
	}

	scopeStr, _ := sset.GetString(ctx, settings.KeyFrontdoorSpoofScope)
	scope := resolver.SpoofScopeAll
	if strings.EqualFold(scopeStr, string(resolver.SpoofScopePrivateOnly)) {
		scope = resolver.SpoofScopePrivateOnly
	}
	cidrStr, _ := sset.GetString(ctx, settings.KeyFrontdoorSpoofAllowCIDR)
	var cidrs []*net.IPNet
	if cidrStr != "" {
		cidrs = resolver.ParseCIDRs(strings.Split(cidrStr, ","))
	}

	policy := &resolver.SpoofPolicy{
		Scope:       scope,
		AllowCIDR:   cidrs,
		TTL:         60,
	}
	if v4 := ip.To4(); v4 != nil {
		policy.ServerIP4 = v4
	} else {
		policy.ServerIP6 = ip
	}
	res.SetSpoofPolicy(policy)
	logger.Info("frontdoor: spoof enabled",
		"scope", scope, "server_ip", ip.String(), "allow_cidr_count", len(cidrs))
	return nil
}

// startProxyForwarders spins up sniforward + quicforward per settings.
// Both are independent; either can be off without affecting the other.
func startProxyForwarders(ctx context.Context, sset *settings.Store, panelDomain, panelBackendUDP string, accessGate *access.Gate, logger *slog.Logger) error {
	sniOn, _ := sset.GetBool(ctx, settings.KeyFrontdoorSNIForwardEnabled)
	quicOn, _ := sset.GetBool(ctx, settings.KeyFrontdoorQUICForwardEnabled)

	if sniOn {
		panelBackendTCP := defaultPanelBackendTCP
		if v, _ := sset.GetString(ctx, settings.KeyFrontdoorPanelBackendTCP); v != "" {
			panelBackendTCP = v
		}
		sni := sniforward.New(sniforward.Config{
			Listen:       ":443",
			PanelDomain:  panelDomain,
			PanelBackend: panelBackendTCP,
			Gate:         accessGate,
		}, logger)
		if err := sni.Start(ctx); err != nil {
			return fmt.Errorf("sniforward: %w", err)
		}
	}
	if quicOn {
		qf := quicforward.New(quicforward.Config{
			Listen:       ":443",
			PanelDomain:  panelDomain,
			PanelBackend: panelBackendUDP,
			Gate:         accessGate,
		}, logger)
		if err := qf.Start(ctx); err != nil {
			return fmt.Errorf("quicforward: %w", err)
		}
	}
	return nil
}

// startMTProxy USED to spin up the in-tree MTProto proxy per settings.
// That code path is dead — both VPS now run the externally-installed
// 9seconds/mtg systemd service, and the panel drives it via
// internal/mtgctl + internal/api/mtproxy_settings.go. The internal
// proxy/mtproxy package is left on disk pending a follow-up cleanup
// but is no longer wired into the daemon. See task rewire spec.
//
// Kept here (function removed, doc-only stub) so the removal is
// explicit in git blame rather than a silent vanish.

// discoverEgressIP returns the local IPv4 the kernel would pick to
// reach a public destination. UDP "dial" performs a routing-table
// lookup without emitting a packet, then LocalAddr reveals which
// source address the routing decision picked. If that lookup fails
// (no default route on a sandboxed test host, e.g.), return "" so
// applySpoofSettings surfaces a clear error rather than trying an
// address we can't stand behind.
func discoverEgressIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer conn.Close()
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP == nil || la.IP.IsUnspecified() {
		return ""
	}
	if v4 := la.IP.To4(); v4 != nil {
		return v4.String()
	}
	return la.IP.String()
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
