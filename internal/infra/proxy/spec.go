// Package proxy turns a profile's proxy string into a live local HTTP proxy,
// backed by sing-box.
//
// Chrome cannot authenticate a proxy from the command line and cannot speak
// SOCKS auth at all, let alone vless or hysteria2. So every profile's upstream —
// whatever protocol it really is — is fronted by an unauthenticated HTTP
// listener on loopback, and Chrome is pointed at that. sing-box performs the
// real handshake behind it.
package proxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// ErrUnsupportedScheme is returned for a proxy URL whose protocol we cannot map
// onto a sing-box outbound.
var ErrUnsupportedScheme = errors.New("unsupported proxy scheme")

// ErrMissingHost is returned when a proxy URL carries no host:port.
var ErrMissingHost = errors.New("proxy is missing host:port")

// ErrMalformed is returned when a URL matches a known scheme but its required
// fields are absent or unparseable.
var ErrMalformed = errors.New("malformed proxy URL")

// Outbound is a sing-box outbound object. It is a plain map rather than a typed
// struct on purpose: sing-box's option structs shift between minor releases,
// while the JSON schema is its documented, stable interface.
type Outbound map[string]any

// Keys in the sing-box outbound schema, named because several appear in more
// than one protocol mapping.
const (
	keyType    = "type"
	keyEnabled = "enabled"
)

// Scheme names shared between the URL switch and the outbound types they map to.
const (
	schemeHTTP = "http"
)

// outboundTag is the name every generated outbound answers to. Nothing routes by
// tag here — there is exactly one — but sing-box requires it.
const outboundTag = "upstream"

// ParseSpec converts a profile's proxy string into a sing-box outbound.
//
// Two forms are accepted. A share link — the `vless://…` or `socks5://…` string
// a provider hands out — covers what people actually have. A raw sing-box
// outbound JSON object (anything starting with "{") is the escape hatch for what
// share links cannot express: multiplex, fragment, custom dialers, and any
// protocol whose link format we do not parse yet.
func ParseSpec(raw string) (Outbound, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("%w: empty", ErrMalformed)
	}

	if strings.HasPrefix(raw, "{") {
		return parseRawJSON(raw)
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}

	return fromURL(u)
}

// parseRawJSON accepts a sing-box outbound object verbatim, forcing the tag so
// the runner can always find it.
func parseRawJSON(raw string) (Outbound, error) {
	var out Outbound
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrMalformed, err)
	}
	if _, ok := out[keyType]; !ok {
		return nil, fmt.Errorf(`%w: raw outbound JSON needs a "type"`, ErrMalformed)
	}
	out["tag"] = outboundTag

	return out, nil
}

func fromURL(u *url.URL) (Outbound, error) {
	switch strings.ToLower(u.Scheme) {
	case "socks5", "socks5h", "socks":
		return socksOutbound(u)
	case schemeHTTP, "https":
		return httpOutbound(u)
	case "trojan":
		return trojanOutbound(u)
	case "vless":
		return vlessOutbound(u)
	case "ss":
		return shadowsocksOutbound(u)
	default:
		return nil, fmt.Errorf("%w: %q (use a raw sing-box outbound JSON object for protocols without a link form)",
			ErrUnsupportedScheme, u.Scheme)
	}
}

// hostPort splits a URL's authority into the server/port pair every sing-box
// outbound needs.
func hostPort(u *url.URL) (string, uint16, error) {
	if u.Host == "" {
		return "", 0, fmt.Errorf("%w: %q", ErrMissingHost, u.Redacted())
	}

	host, portStr, err := net.SplitHostPort(u.Host)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %q needs host:port", ErrMissingHost, u.Redacted())
	}

	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return "", 0, fmt.Errorf("%w: bad port in %q", ErrMalformed, u.Redacted())
	}

	return host, uint16(port), nil
}

// base adds the fields every outbound shares.
func base(kind, host string, port uint16) Outbound {
	return Outbound{
		keyType:       kind,
		"tag":         outboundTag,
		"server":      host,
		"server_port": int(port),
	}
}

func socksOutbound(u *url.URL) (Outbound, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}

	out := base("socks", host, port)
	out["version"] = "5"
	if u.User != nil {
		pass, _ := u.User.Password()
		out["username"] = u.User.Username()
		out["password"] = pass
	}

	return out, nil
}

func httpOutbound(u *url.URL) (Outbound, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}

	out := base(schemeHTTP, host, port)
	if u.User != nil {
		pass, _ := u.User.Password()
		out["username"] = u.User.Username()
		out["password"] = pass
	}
	// An https:// proxy means TLS to the proxy itself, not to the origin.
	if strings.EqualFold(u.Scheme, "https") {
		out["tls"] = map[string]any{keyEnabled: true, "server_name": host}
	}

	return out, nil
}

// trojanOutbound maps trojan://password@host:port?sni=…&type=…
func trojanOutbound(u *url.URL) (Outbound, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, fmt.Errorf("%w: trojan link needs a password", ErrMalformed)
	}

	out := base("trojan", host, port)
	out["password"] = u.User.Username()

	q := u.Query()
	// Trojan is TLS-only, so TLS is on unless the link explicitly says otherwise.
	out["tls"] = tlsOptions(q, host, true)
	if t := transportOptions(q); t != nil {
		out["transport"] = t
	}

	return out, nil
}

// vlessOutbound maps vless://uuid@host:port?encryption=none&security=tls|reality&…
func vlessOutbound(u *url.URL) (Outbound, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	if u.User == nil || u.User.Username() == "" {
		return nil, fmt.Errorf("%w: vless link needs a uuid", ErrMalformed)
	}

	out := base("vless", host, port)
	out["uuid"] = u.User.Username()

	q := u.Query()
	if flow := q.Get("flow"); flow != "" {
		out["flow"] = flow
	}
	security := strings.ToLower(q.Get("security"))
	if security == "tls" || security == "reality" || security == "xtls" {
		out["tls"] = tlsOptions(q, host, true)
	}
	if t := transportOptions(q); t != nil {
		out["transport"] = t
	}

	return out, nil
}

// shadowsocksOutbound maps the SIP002 form, ss://base64(method:password)@host:port.
func shadowsocksOutbound(u *url.URL) (Outbound, error) {
	host, port, err := hostPort(u)
	if err != nil {
		return nil, err
	}
	if u.User == nil {
		return nil, fmt.Errorf("%w: shadowsocks link needs method:password", ErrMalformed)
	}

	method := u.User.Username()

	password, ok := u.User.Password()
	if !ok {
		// SIP002 base64-encodes "method:password" into the userinfo.
		decoded, err := decodeBase64(method)
		if err != nil {
			return nil, fmt.Errorf("%w: shadowsocks userinfo is neither method:password nor base64", ErrMalformed)
		}
		method, password, _ = strings.Cut(decoded, ":")
		if password == "" {
			return nil, fmt.Errorf("%w: shadowsocks link needs method:password", ErrMalformed)
		}
	}

	out := base("shadowsocks", host, port)
	out["method"] = method
	out["password"] = password

	return out, nil
}

// decodeBase64 accepts both padded and raw URL-safe base64, which share links
// use interchangeably.
func decodeBase64(s string) (string, error) {
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding,
	} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}

	return "", fmt.Errorf("%w: not base64", ErrMalformed)
}

// tlsOptions builds the TLS block from a share link's query parameters. Note
// utls: a share link's fp=chrome is a request to mimic Chrome's TLS ClientHello,
// which is exactly the point for an anti-detect profile — passing it through
// matters as much as the address does.
func tlsOptions(q url.Values, host string, enabled bool) map[string]any {
	tls := map[string]any{keyEnabled: enabled}

	sni := q.Get("sni")
	if sni == "" {
		sni = q.Get("peer")
	}
	if sni == "" {
		sni = host
	}
	tls["server_name"] = sni

	if alpn := q.Get("alpn"); alpn != "" {
		tls["alpn"] = strings.Split(alpn, ",")
	}
	if q.Get("allowInsecure") == "1" || strings.EqualFold(q.Get("insecure"), "true") {
		tls["insecure"] = true
	}
	if fp := q.Get("fp"); fp != "" {
		tls["utls"] = map[string]any{keyEnabled: true, "fingerprint": fp}
	}
	if pbk := q.Get("pbk"); pbk != "" {
		reality := map[string]any{keyEnabled: true, "public_key": pbk}
		if sid := q.Get("sid"); sid != "" {
			reality["short_id"] = sid
		}
		tls["reality"] = reality
	}

	return tls
}

// transportOptions maps the stream settings a link may carry. A nil result means
// plain TCP, which sing-box treats as the absence of a transport block.
func transportOptions(q url.Values) map[string]any {
	switch strings.ToLower(q.Get("type")) {
	case "ws":
		ws := map[string]any{keyType: "ws"}
		if path := q.Get("path"); path != "" {
			ws["path"] = path
		}
		if hostHeader := q.Get("host"); hostHeader != "" {
			ws["headers"] = map[string]any{"Host": hostHeader}
		}

		return ws
	case "grpc":
		grpc := map[string]any{keyType: "grpc"}
		if name := q.Get("serviceName"); name != "" {
			grpc["service_name"] = name
		}

		return grpc
	case "http":
		h := map[string]any{keyType: schemeHTTP}
		if hostHeader := q.Get("host"); hostHeader != "" {
			h["host"] = []string{hostHeader}
		}
		if path := q.Get("path"); path != "" {
			h["path"] = path
		}

		return h
	default:
		return nil
	}
}
