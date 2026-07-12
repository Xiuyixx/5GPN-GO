package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/Xiuyixx/5GPN-Go/internal/resolver"
	"github.com/Xiuyixx/5GPN-Go/internal/settings"
)

// applySpoofSettings publishes a validated SpoofPolicy from panel settings.
func applySpoofSettings(ctx context.Context, res *resolver.Resolver, sset *settings.Store, panelDomain string, logger *slog.Logger) error {
	on, err := sset.GetBool(ctx, settings.KeyFrontdoorSpoofEnabled)
	if err != nil {
		return err
	}
	if !on {
		return nil
	}
	_ = panelDomain
	ipStr, err := sset.GetString(ctx, settings.KeyFrontdoorSpoofServerIP)
	if err != nil {
		return err
	}
	if ipStr == "" {
		ipStr = discoverEgressIP()
	}
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return fmt.Errorf("spoof: server_ip not set and autodetect failed")
	}

	scopeStr, err := sset.GetString(ctx, settings.KeyFrontdoorSpoofScope)
	if err != nil {
		return err
	}
	scopeStr = strings.ToLower(strings.TrimSpace(scopeStr))
	if scopeStr == "" {
		scopeStr = string(resolver.SpoofScopeAll)
	}
	var scope resolver.SpoofScope
	switch scopeStr {
	case string(resolver.SpoofScopeAll):
		scope = resolver.SpoofScopeAll
	case string(resolver.SpoofScopePrivateOnly):
		scope = resolver.SpoofScopePrivateOnly
	default:
		return fmt.Errorf("spoof: invalid scope %q", scopeStr)
	}
	cidrStr, err := sset.GetString(ctx, settings.KeyFrontdoorSpoofAllowCIDR)
	if err != nil {
		return err
	}
	var cidrs []*net.IPNet
	if cidrStr != "" {
		for _, raw := range strings.Split(cidrStr, ",") {
			_, cidr, parseErr := net.ParseCIDR(strings.TrimSpace(raw))
			if parseErr != nil {
				return fmt.Errorf("spoof: invalid allow CIDR %q: %w", raw, parseErr)
			}
			cidrs = append(cidrs, cidr)
		}
	}

	policy := &resolver.SpoofPolicy{Scope: scope, AllowCIDR: cidrs, TTL: 60}
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

// discoverEgressIP asks the routing table which source address would be used
// for a public destination. UDP Dial performs no application data exchange.
func discoverEgressIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la.IP == nil || la.IP.IsUnspecified() {
		return ""
	}
	if v4 := la.IP.To4(); v4 != nil {
		return v4.String()
	}
	return la.IP.String()
}
