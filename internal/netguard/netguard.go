// Package netguard contains the outbound-network policy shared by fetchers
// and transparent forwarders. User-controlled destinations may resolve only
// to public, globally-routable addresses.
package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxRedirects = 10

var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

// IsPublicIP reports whether ip is suitable for an untrusted outbound
// destination. IPv4-mapped IPv6 addresses are normalized before evaluation.
func IsPublicIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() ||
		addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsUnspecified() {
		return false
	}
	return !carrierGradeNAT.Contains(addr)
}

// ValidateHTTPURL accepts only absolute HTTP(S) URLs without credentials.
func ValidateHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("URL scheme must be http or https")
	}
	if u.Hostname() == "" {
		return nil, errors.New("URL hostname is required")
	}
	if u.User != nil {
		return nil, errors.New("URL credentials are not allowed")
	}
	return u, nil
}

// ResolvePublic resolves host exactly once and rejects non-public answers.
// Returning concrete addresses lets callers connect to the checked result
// rather than asking the resolver a second time (DNS-rebinding defense).
func ResolvePublic(ctx context.Context, resolver *net.Resolver, host string) ([]net.IP, error) {
	host = strings.TrimSpace(strings.TrimSuffix(host, "."))
	if host == "" {
		return nil, errors.New("empty hostname")
	}
	if literal := net.ParseIP(host); literal != nil {
		if !IsPublicIP(literal) {
			return nil, fmt.Errorf("destination %s is not public", host)
		}
		return []net.IP{literal}, nil
	}
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	out := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		if !IsPublicIP(addr.IP) {
			return nil, fmt.Errorf("destination %s resolved to non-public address %s", host, addr.IP)
		}
		out = append(out, append(net.IP(nil), addr.IP...))
	}
	return out, nil
}

// DialPublicContext resolves and connects to a checked concrete address.
func DialPublicContext(ctx context.Context, resolver *net.Resolver, dialer *net.Dialer, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid destination %q: %w", address, err)
	}
	ips, err := ResolvePublic(ctx, resolver, host)
	if err != nil {
		return nil, err
	}
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	var errs []error
	for _, ip := range ips {
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		errs = append(errs, dialErr)
	}
	return nil, fmt.Errorf("dial %s: %w", address, errors.Join(errs...))
}

// NewHTTPClient returns an HTTP client that applies the public-address policy
// to the initial request and every redirect. ProxyFromEnvironment is omitted
// deliberately: a proxy would move destination resolution outside this guard.
func NewHTTPClient(timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return DialPublicContext(ctx, net.DefaultResolver, dialer, network, address)
		},
		ForceAttemptHTTP2: true,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			_, err := ValidateHTTPURL(req.URL.String())
			return err
		},
	}
}
