// Package exit models proxy egress protocols and parses share URIs into
// mihomo-shaped Go structs.
//
// The parsers mirror 5GPN-X/mihomo-exit-config.py behavior 1:1: 10
// protocols supported (ss / ss2022 shares the ss path / socks5 / socks5h
// / vmess / trojan / vless / hysteria2 / hy2 / tuic / anytls / http /
// https). The unit tests exercise every protocol's URI parser.
package exit

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Proxy is the mihomo proxy object we emit; keys match the mihomo schema.
type Proxy map[string]any

// Parse identifies the URI scheme and dispatches to a protocol parser.
// Returns a mihomo Proxy struct (name="out") ready to inject into a
// config document.
func Parse(uri string) (Proxy, error) {
	uri = strings.TrimSpace(uri)
	low := strings.ToLower(uri)

	switch {
	case strings.HasPrefix(low, "ss://"):
		return parseSS(uri)
	case strings.HasPrefix(low, "vmess://"):
		return parseVMess(uri)
	case strings.HasPrefix(low, "trojan://"):
		return parseTrojan(uri)
	case strings.HasPrefix(low, "vless://"):
		return parseVLess(uri)
	case strings.HasPrefix(low, "hysteria2://") || strings.HasPrefix(low, "hy2://"):
		return parseHysteria2(uri)
	case strings.HasPrefix(low, "tuic://"):
		return parseTUIC(uri)
	case strings.HasPrefix(low, "anytls://"):
		return parseAnyTLS(uri)
	case strings.HasPrefix(low, "socks5h://") || strings.HasPrefix(low, "socks5://") || strings.HasPrefix(low, "socks://"):
		return parseSocks(uri)
	case strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://"):
		return parseHTTP(uri)
	}
	return nil, fmt.Errorf("exit: unsupported URI scheme: %s", firstScheme(uri))
}

func firstScheme(uri string) string {
	if i := strings.Index(uri, "://"); i > 0 {
		return uri[:i]
	}
	return uri
}

// b64AnyDecode tries urlsafe + standard base64, tolerating missing padding.
func b64AnyDecode(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty payload")
	}
	pad := (4 - len(s)%4) % 4
	padded := s + strings.Repeat("=", pad)
	for _, dec := range []func(string) ([]byte, error){
		base64.URLEncoding.DecodeString,
		base64.StdEncoding.DecodeString,
	} {
		if v, err := dec(padded); err == nil {
			return string(v), nil
		}
	}
	return "", errors.New("not base64")
}

var hostPortRE = regexp.MustCompile(`^\[(.+)\]:(\d+)$|^(.+):(\d+)$`)

// parseHostPort splits host:port, honoring [ipv6]:port.
func parseHostPort(value string) (string, int, error) {
	value = strings.SplitN(value, "/", 2)[0]
	value = strings.SplitN(value, "?", 2)[0]
	value = strings.SplitN(value, "#", 2)[0]
	value = strings.TrimSpace(value)
	m := hostPortRE.FindStringSubmatch(value)
	if m == nil {
		return "", 0, fmt.Errorf("cannot parse host:port from %q", value)
	}
	host := m[1]
	port := m[2]
	if host == "" {
		host = m[3]
		port = m[4]
	}
	if host == "" {
		return "", 0, errors.New("missing host")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return "", 0, fmt.Errorf("bad port %q", port)
	}
	return host, p, nil
}

func portOrDefault(u *url.URL, def int) (int, error) {
	if u.Port() == "" {
		return def, nil
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p < 1 || p > 65535 {
		return 0, fmt.Errorf("bad port %q", u.Port())
	}
	return p, nil
}

func queryFirst(u *url.URL) map[string]string {
	out := map[string]string{}
	for k, vs := range u.Query() {
		if len(vs) > 0 {
			out[k] = vs[0]
		}
	}
	return out
}

func truthy(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func setTLS(p Proxy, servername string, insecure bool) {
	p["tls"] = true
	if servername != "" {
		p["servername"] = servername
	}
	if insecure {
		p["skip-cert-verify"] = true
	}
}

func setTransport(p Proxy, network, host, path, service string) {
	network = strings.ToLower(network)
	if network == "" {
		network = "tcp"
	}
	switch network {
	case "ws", "websocket":
		p["network"] = "ws"
		opts := map[string]any{"path": unescape(path, "/")}
		if host != "" {
			opts["headers"] = map[string]string{"Host": host}
		}
		p["ws-opts"] = opts
	case "grpc":
		p["network"] = "grpc"
		name := service
		if name == "" {
			name = strings.TrimPrefix(unescape(path, ""), "/")
		}
		p["grpc-opts"] = map[string]any{"grpc-service-name": name}
	case "http", "h2":
		p["network"] = "h2"
		hosts := []string{}
		if host != "" {
			hosts = []string{host}
		}
		p["h2-opts"] = map[string]any{"path": unescape(path, "/"), "host": hosts}
	}
}

func unescape(s, def string) string {
	if s == "" {
		return def
	}
	if v, err := url.QueryUnescape(s); err == nil {
		return v
	}
	return s
}

var ssCredsRE = regexp.MustCompile(`^[a-z0-9-]+$`)

func parseSS(uri string) (Proxy, error) {
	rest := strings.TrimPrefix(uri, "ss://")
	rest = strings.SplitN(rest, "#", 2)[0]
	rest = strings.SplitN(rest, "?", 2)[0]

	var method, password, server string
	if strings.Contains(rest, "@") {
		i := strings.LastIndex(rest, "@")
		userinfo, srv := rest[:i], rest[i+1:]
		if decoded, err := b64AnyDecode(userinfo); err == nil && strings.Contains(decoded, ":") {
			parts := strings.SplitN(decoded, ":", 2)
			if ssCredsRE.MatchString(parts[0]) {
				method = parts[0]
				password = unescape(parts[1], "")
			}
		}
		if method == "" {
			userinfo, err := url.QueryUnescape(userinfo)
			if err != nil || !strings.Contains(userinfo, ":") {
				return nil, errors.New("cannot parse ss:// credentials")
			}
			parts := strings.SplitN(userinfo, ":", 2)
			method = parts[0]
			password = parts[1]
		}
		server = srv
	} else {
		dec, err := b64AnyDecode(rest)
		if err != nil {
			return nil, errors.New("invalid ss:// payload")
		}
		if !strings.Contains(dec, "@") || !strings.Contains(dec, ":") {
			return nil, errors.New("invalid legacy ss:// payload")
		}
		i := strings.LastIndex(dec, "@")
		creds, srv := dec[:i], dec[i+1:]
		parts := strings.SplitN(creds, ":", 2)
		method, password, server = parts[0], parts[1], srv
	}
	host, port, err := parseHostPort(server)
	if err != nil {
		return nil, fmt.Errorf("ss: %w", err)
	}
	return Proxy{
		"name": "out", "type": "ss", "server": host, "port": port,
		"cipher": method, "password": password, "udp": true,
	}, nil
}

func parseSocks(uri string) (Proxy, error) {
	rest := regexp.MustCompile(`(?i)^socks(?:5h|5)?://`).ReplaceAllString(uri, "")
	var userinfo, hostport string
	if strings.Contains(rest, "@") {
		i := strings.LastIndex(rest, "@")
		userinfo, hostport = rest[:i], rest[i+1:]
	} else {
		hostport = rest
	}
	host, port, err := parseHostPort(hostport)
	if err != nil {
		return nil, fmt.Errorf("socks: %w", err)
	}
	p := Proxy{"name": "out", "type": "socks5", "server": host, "port": port, "udp": true}
	if userinfo != "" {
		if u, err := url.QueryUnescape(userinfo); err == nil {
			userinfo = u
		}
		var user, pass string
		if idx := strings.Index(userinfo, ":"); idx >= 0 {
			user = userinfo[:idx]
			pass = userinfo[idx+1:]
		} else {
			user = userinfo
		}
		p["username"] = user
		if pass != "" {
			p["password"] = pass
		}
	}
	return p, nil
}

func parseVMess(uri string) (Proxy, error) {
	body := strings.TrimPrefix(uri, "vmess://")
	decoded, err := b64AnyDecode(body)
	if err != nil {
		return nil, fmt.Errorf("vmess: %w", err)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(decoded), &data); err != nil {
		return nil, fmt.Errorf("vmess payload: %w", err)
	}
	host, _ := data["add"].(string)
	if host == "" {
		return nil, errors.New("vmess: missing host")
	}
	port := 443
	switch v := data["port"].(type) {
	case string:
		p, err := strconv.Atoi(v)
		if err == nil {
			port = p
		}
	case float64:
		port = int(v)
	}
	aid := 0
	switch v := data["aid"].(type) {
	case string:
		if p, err := strconv.Atoi(v); err == nil {
			aid = p
		}
	case float64:
		aid = int(v)
	}
	cipher := "auto"
	if v, ok := data["scy"].(string); ok && v != "" {
		cipher = v
	}
	uuid, _ := data["id"].(string)
	proxy := Proxy{
		"name": "out", "type": "vmess", "server": host, "port": port,
		"uuid": uuid, "alterId": aid, "cipher": cipher, "udp": true,
	}
	if tls, _ := data["tls"].(string); strings.EqualFold(tls, "tls") || strings.EqualFold(tls, "true") || tls == "1" {
		sni, _ := data["sni"].(string)
		if sni == "" {
			if h, _ := data["host"].(string); h != "" {
				sni = h
			} else {
				sni = host
			}
		}
		setTLS(proxy, sni, false)
	}
	net, _ := data["net"].(string)
	pth, _ := data["path"].(string)
	hh, _ := data["host"].(string)
	setTransport(proxy, net, hh, pth, "")
	return proxy, nil
}

func parseTrojan(uri string) (Proxy, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("trojan: missing host")
	}
	port, err := portOrDefault(u, 443)
	if err != nil {
		return nil, err
	}
	q := queryFirst(u)
	pw := ""
	if u.User != nil {
		pw, _ = url.QueryUnescape(u.User.Username())
	}
	proxy := Proxy{
		"name": "out", "type": "trojan", "server": host,
		"port": port, "password": pw, "udp": true,
	}
	sni := q["sni"]
	if sni == "" {
		sni = q["peer"]
	}
	if sni == "" {
		sni = host
	}
	setTLS(proxy, sni, truthy(q["allowInsecure"]))
	setTransport(proxy, q["type"], q["host"], q["path"], firstNonEmpty(q["serviceName"], q["service_name"]))
	return proxy, nil
}

func parseVLess(uri string) (Proxy, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("vless: missing host")
	}
	port, err := portOrDefault(u, 443)
	if err != nil {
		return nil, err
	}
	q := queryFirst(u)
	uuid := ""
	if u.User != nil {
		uuid, _ = url.QueryUnescape(u.User.Username())
	}
	proxy := Proxy{
		"name": "out", "type": "vless", "server": host,
		"port": port, "uuid": uuid, "udp": true,
	}
	if v := q["flow"]; v != "" {
		proxy["flow"] = v
	}
	security := strings.ToLower(q["security"])
	if security == "" {
		security = "tls"
	}
	if security == "tls" || security == "reality" {
		sni := q["sni"]
		if sni == "" {
			sni = host
		}
		setTLS(proxy, sni, truthy(q["allowInsecure"]))
	}
	if security == "reality" {
		pbk := firstNonEmpty(q["pbk"], q["public_key"])
		if pbk == "" {
			return nil, errors.New("vless reality URI missing public key (pbk)")
		}
		proxy["reality-opts"] = map[string]any{
			"public-key": pbk,
			"short-id":   firstNonEmpty(q["sid"], q["short_id"]),
		}
		fp := q["fp"]
		if fp == "" {
			fp = "chrome"
		}
		proxy["client-fingerprint"] = fp
	}
	setTransport(proxy, firstNonEmpty(q["type"], q["transport"]), q["host"], q["path"],
		firstNonEmpty(q["serviceName"], q["service_name"]))
	return proxy, nil
}

func parseHysteria2(uri string) (Proxy, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("hysteria2: missing host")
	}
	port, err := portOrDefault(u, 443)
	if err != nil {
		return nil, err
	}
	q := queryFirst(u)
	pw := ""
	if u.User != nil {
		pw, _ = url.QueryUnescape(u.User.Username())
	}
	sni := q["sni"]
	if sni == "" {
		sni = host
	}
	p := Proxy{
		"name": "out", "type": "hysteria2", "server": host,
		"port": port, "password": pw, "udp": true,
		"sni": sni, "skip-cert-verify": truthy(q["insecure"]),
	}
	if o := q["obfs"]; o != "" {
		p["obfs"] = o
		p["obfs-password"] = firstNonEmpty(q["obfs-password"], q["obfs_password"])
	}
	return p, nil
}

func parseTUIC(uri string) (Proxy, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("tuic: missing host")
	}
	port, err := portOrDefault(u, 443)
	if err != nil {
		return nil, err
	}
	q := queryFirst(u)
	var uuid, password string
	if u.User != nil {
		raw, _ := url.QueryUnescape(u.User.Username())
		if strings.Contains(raw, ":") {
			parts := strings.SplitN(raw, ":", 2)
			uuid, password = parts[0], parts[1]
		} else {
			uuid = raw
			if p, ok := u.User.Password(); ok {
				password, _ = url.QueryUnescape(p)
			}
		}
	}
	sni := q["sni"]
	if sni == "" {
		sni = host
	}
	udpRelayMode := q["udp_relay_mode"]
	if udpRelayMode == "" {
		udpRelayMode = "native"
	}
	cc := q["congestion_control"]
	if cc == "" {
		cc = "bbr"
	}
	return Proxy{
		"name": "out", "type": "tuic", "server": host,
		"port": port, "uuid": uuid, "password": password, "udp": true,
		"sni": sni, "skip-cert-verify": truthy(q["allow_insecure"]),
		"udp-relay-mode": udpRelayMode, "congestion-controller": cc,
	}, nil
}

func parseAnyTLS(uri string) (Proxy, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("anytls: missing host")
	}
	port, err := portOrDefault(u, 443)
	if err != nil {
		return nil, err
	}
	q := queryFirst(u)
	pw := ""
	if u.User != nil {
		pw, _ = url.QueryUnescape(u.User.Username())
	}
	sni := q["sni"]
	if sni == "" {
		sni = host
	}
	return Proxy{
		"name": "out", "type": "anytls", "server": host,
		"port": port, "password": pw, "udp": true,
		"sni": sni, "skip-cert-verify": truthy(q["insecure"]),
	}, nil
}

func parseHTTP(uri string) (Proxy, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, err
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("http: missing host")
	}
	def := 80
	if strings.EqualFold(u.Scheme, "https") {
		def = 443
	}
	port, err := portOrDefault(u, def)
	if err != nil {
		return nil, err
	}
	p := Proxy{"name": "out", "type": "http", "server": host, "port": port}
	if u.User != nil {
		user, _ := url.QueryUnescape(u.User.Username())
		p["username"] = user
		if pw, ok := u.User.Password(); ok {
			pu, _ := url.QueryUnescape(pw)
			p["password"] = pu
		}
	}
	if strings.EqualFold(u.Scheme, "https") {
		setTLS(p, host, false)
	}
	return p, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
