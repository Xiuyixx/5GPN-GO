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

	"github.com/Xiuyixx/5GPN-Go/internal/api"
	"github.com/Xiuyixx/5GPN-Go/internal/config"
	"github.com/Xiuyixx/5GPN-Go/internal/db"
	"github.com/Xiuyixx/5GPN-Go/internal/orchestrator"
	"github.com/Xiuyixx/5GPN-Go/internal/tgbot"
	"github.com/Xiuyixx/5GPN-Go/internal/web"
)

var version = "0.0.0-m1"

func main() {
	configPath := flag.String("config", "/etc/5gpn/config.yaml", "path to config.yaml")
	dataDir := flag.String("data", "/var/lib/5gpn", "state directory (SQLite, snapshots, keys)")
	listenAddr := flag.String("listen", "", "override server address (empty = use config)")
	insecure := flag.Bool("insecure", false, "serve HTTP instead of HTTPS (dev only)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(logger, *configPath, *dataDir, *listenAddr, *insecure); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, configPath, dataDir, listenOverride string, insecure bool) error {
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

	setupToken := ""
	if n, _ := db.CountPanelUsers(dbHandle); n == 0 {
		setupToken = randomHex(32)
		logger.Warn("no panel user found — one-time setup token below")
		fmt.Printf("\n===============================================================\n")
		fmt.Printf("5GPN SETUP TOKEN (valid until first successful bootstrap):\n\n  %s\n\n", setupToken)
		fmt.Printf("POST /api/v1/bootstrap { token, username, password } to claim.\n")
		fmt.Printf("===============================================================\n\n")
	}

	orch := orchestrator.Orchestrator(&orchestrator.NoOp{Logger: logger})
	// M2 will select Systemd on Linux hosts once the render layer lands.

	srv := api.New(dbHandle, api.Config{
		SessionTTL:     cfg.Panel.SessionTTL,
		LoginPerMinute: cfg.Panel.RateLimit.LoginPerMinute,
		LockoutMinutes: cfg.Panel.RateLimit.LockoutMinutes,
		JWTSecret:      jwtSecret,
		Issuer:         cfg.Server.Domain,
		SetupToken:     setupToken,
		WebFS:          web.FS,
		Orchestrator:   orch,
	}, logger)

	addr := listenOverride
	if addr == "" {
		addr = fmt.Sprintf("%s:%d", cfg.Server.PanelBind, cfg.Server.PanelPort)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	cert, key := cfg.Server.TLS.Cert, cfg.Server.TLS.Key
	if insecure {
		cert, key = "", ""
	}
	// TG bot (optional — starts only when config.tgbot.token is set).
	if cfg.TGBot.Token != "" {
		bot, err := tgbot.New(tgbot.Config{
			Token:        cfg.TGBot.Token,
			AdminChatIDs: cfg.TGBot.AdminChatIDs,
			Handlers: &tgbot.DefaultHandlers{
				DB:     dbHandle,
				Logger: logger,
			},
			Logger: logger,
		})
		if err != nil {
			if errors.Is(err, tgbot.ErrBotDisabled) {
				logger.Info("tgbot disabled (empty token after env expansion)")
			} else {
				logger.Warn("tgbot init failed — panel still serves", "err", err)
			}
		} else {
			go func() {
				if err := bot.Serve(ctx); err != nil {
					logger.Warn("tgbot.Serve exited", "err", err)
				}
			}()
		}
	}

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
