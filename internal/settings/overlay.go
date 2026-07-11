// Boot-time overlay of panel_settings on top of the YAML Config.
//
// Contract: /etc/5gpn/config.yaml stays authoritative for anything NOT
// set via the panel. Whenever a key is present in the panel_settings
// table AND holds a non-zero value, it overrides the YAML equivalent.
// Empty string, zero int, and empty slice all mean "leave the YAML
// value alone" — this keeps the wizard's "skipped" step semantics
// coherent (a user who never touched WAShim in the wizard should not
// silently clobber the YAML WAShim block).

package settings

import (
	"context"
	"errors"
	"fmt"

	"github.com/Xiuyixx/5GPN-Go/internal/config"
)

// OverlayConfig mutates cfg in place, applying panel_settings values
// wherever they are non-zero. Returns the first read/decode error; on
// success cfg is safe to hand to consumers (api.New, tgbot.Manager,
// core.Assemble).
func OverlayConfig(ctx context.Context, store *Store, cfg *config.Config) error {
	if store == nil || cfg == nil {
		return nil
	}

	if v, err := store.GetString(ctx, KeyServerDomain); err != nil {
		return fmt.Errorf("overlay server.domain: %w", err)
	} else if v != "" {
		cfg.Server.Domain = v
	}
	if v, err := store.GetString(ctx, KeyServerPanelBind); err != nil {
		return fmt.Errorf("overlay server.panel_bind: %w", err)
	} else if v != "" {
		cfg.Server.PanelBind = v
	}
	if v, err := store.GetInt(ctx, KeyServerPanelPort); err != nil {
		return fmt.Errorf("overlay server.panel_port: %w", err)
	} else if v != 0 {
		cfg.Server.PanelPort = v
	}

	if v, err := store.GetString(ctx, KeyTGBotToken); err != nil {
		return fmt.Errorf("overlay tgbot.token: %w", err)
	} else if v != "" {
		cfg.TGBot.Token = v
	}
	var ids []int64
	if err := store.GetJSON(ctx, KeyTGBotAdminChats, &ids); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("overlay tgbot.admin_chat_ids: %w", err)
	}
	if len(ids) > 0 {
		cfg.TGBot.AdminChatIDs = ids
	}

	if v, err := store.GetString(ctx, KeyWAShimListen); err != nil {
		return fmt.Errorf("overlay washim.listen: %w", err)
	} else if v != "" {
		cfg.Proxy.WAShim.Listen = v
	}
	if v, err := store.GetInt(ctx, KeyWAShimPort); err != nil {
		return fmt.Errorf("overlay washim.port: %w", err)
	} else if v != 0 {
		cfg.Proxy.WAShim.Port = v
	}
	if v, err := store.GetString(ctx, KeyWAShimBackend); err != nil {
		return fmt.Errorf("overlay washim.backend: %w", err)
	} else if v != "" {
		cfg.Proxy.WAShim.Backend = v
	}
	if v, err := store.GetString(ctx, KeyWAShimWAHost); err != nil {
		return fmt.Errorf("overlay washim.wa_host: %w", err)
	} else if v != "" {
		cfg.Proxy.WAShim.WAHost = v
	}
	var cidrs []string
	if err := store.GetJSON(ctx, KeyWAShimAllowCIDR, &cidrs); err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("overlay washim.allow_cidr: %w", err)
	}
	if len(cidrs) > 0 {
		cfg.Proxy.WAShim.AllowCIDR = cidrs
	}

	return nil
}
