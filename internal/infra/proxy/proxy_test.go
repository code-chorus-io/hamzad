package proxy_test

import (
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/things-go/go-socks5"

	"github.com/1995parham/koochooloologin/internal/infra/proxy"
)

// Shared fixtures, factored out so the table below reads as data rather than
// repeated literals.
const (
	testHost  = "1.2.3.4"
	keyPass   = "password"
	keyServer = "server"
	keyPort   = "server_port"
	keyKind   = "type"
)

// TestParseSpecMapsShareLinks pins the share-link forms a provider hands out
// onto sing-box outbounds. A wrong mapping here does not fail loudly — it
// produces a proxy that connects with the wrong SNI or fingerprint, which is
// exactly the silent mismatch this tool exists to avoid.
func TestParseSpecMapsShareLinks(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		raw  string
		want map[string]any
	}{
		"socks5 with credentials": {
			raw: "socks5://user:pass@" + testHost + ":1080",
			want: map[string]any{
				keyKind: "socks", keyServer: testHost, keyPort: 1080,
				"username": "user", keyPass: "pass", "version": "5",
			},
		},
		"http without credentials": {
			raw:  "http://" + testHost + ":8080",
			want: map[string]any{keyKind: "http", keyServer: testHost, keyPort: 8080},
		},
		"trojan": {
			raw: "trojan://secret@example.com:443?sni=cdn.example.com",
			want: map[string]any{
				keyKind: "trojan", keyServer: "example.com", keyPort: 443, keyPass: "secret",
			},
		},
		"vless with reality": {
			raw: "vless://11111111-2222-3333-4444-555555555555@example.com:443" +
				"?security=reality&pbk=abc&sid=ff&fp=chrome&sni=www.microsoft.com&flow=xtls-rprx-vision",
			want: map[string]any{
				keyKind: "vless", keyServer: "example.com", keyPort: 443,
				"uuid": "11111111-2222-3333-4444-555555555555", "flow": "xtls-rprx-vision",
			},
		},
		"shadowsocks sip002": {
			raw: "ss://YWVzLTI1Ni1nY206c2VjcmV0@" + testHost + ":8388",
			want: map[string]any{
				keyKind: "shadowsocks", keyServer: testHost, keyPort: 8388,
				"method": "aes-256-gcm", keyPass: "secret",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got, err := proxy.ParseSpec(tc.raw)
			if err != nil {
				t.Fatalf("ParseSpec(%q): %v", tc.raw, err)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("%s = %#v, want %#v", k, got[k], want)
				}
			}
		})
	}
}

// TestParseSpecCarriesRealityAndFingerprint checks the two fields that matter
// most for staying unnoticed survive the mapping: the REALITY public key, and
// the uTLS fingerprint that makes the TLS ClientHello look like Chrome's.
func TestParseSpecCarriesRealityAndFingerprint(t *testing.T) {
	t.Parallel()

	out, err := proxy.ParseSpec("vless://uuid-here@example.com:443?security=reality&pbk=PUBKEY&sid=01ab&fp=chrome&sni=www.apple.com")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}

	tls, ok := out["tls"].(map[string]any)
	if !ok {
		t.Fatal("reality link produced no tls block")
	}
	if tls["server_name"] != "www.apple.com" {
		t.Errorf("server_name = %v, want the sni from the link", tls["server_name"])
	}

	reality, ok := tls["reality"].(map[string]any)
	if !ok {
		t.Fatal("no reality block")
	}
	if reality["public_key"] != "PUBKEY" || reality["short_id"] != "01ab" {
		t.Errorf("reality = %#v", reality)
	}

	utls, ok := tls["utls"].(map[string]any)
	if !ok || utls["fingerprint"] != "chrome" {
		t.Errorf("utls = %#v, want the chrome fingerprint preserved", tls["utls"])
	}
}

// TestParseSpecAcceptsRawOutboundJSON covers the escape hatch: protocols whose
// link form we do not parse, and options no link can express.
func TestParseSpecAcceptsRawOutboundJSON(t *testing.T) {
	t.Parallel()

	out, err := proxy.ParseSpec(`{"type":"hysteria2","server":"h.example.com","server_port":443,"password":"x"}`)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if out["type"] != "hysteria2" || out["server"] != "h.example.com" {
		t.Errorf("raw JSON not passed through: %#v", out)
	}
	if out["tag"] == nil {
		t.Error("raw JSON outbound must be given a tag so the runner can find it")
	}
}

// TestParseSpecRejectsGarbage keeps a typo from reaching sing-box as a confusing
// runtime error.
func TestParseSpecRejectsGarbage(t *testing.T) {
	t.Parallel()

	for name, raw := range map[string]string{
		"empty":             "",
		"unknown scheme":    "carrierpigeon://host:1",
		"no port":           "socks5://1.2.3.4",
		"vless without id":  "vless://@example.com:443",
		"broken json":       `{"type":`,
		"json without type": `{"server":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if _, err := proxy.ParseSpec(raw); err == nil {
				t.Fatalf("ParseSpec(%q) accepted invalid input", raw)
			}
		})
	}
}

// TestRelayTunnelsThroughAuthenticatedSocks is the end-to-end proof, and the
// reason the whole layer exists: Chrome cannot authenticate a SOCKS proxy, so an
// authenticated upstream is fronted by a plain local HTTP listener. This starts
// a real SOCKS5 server demanding credentials, a real origin behind it, and drives
// a real HTTP client through sing-box.
func TestRelayTunnelsThroughAuthenticatedSocks(t *testing.T) {
	t.Parallel()

	const user, pass = "kel", "s3cret"

	origin := startOrigin(t)
	socksAddr := startSocks5(t, user, pass)

	out, err := proxy.ParseSpec("socks5://" + user + ":" + pass + "@" + socksAddr)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}

	relay, err := proxy.Start(t.Context(), out)
	if err != nil {
		t.Fatalf("proxy.Start: %v", err)
	}
	defer func() { _ = relay.Close() }()

	body := getThrough(t, relay.Addr(), origin)
	if body != "hello from origin" {
		t.Errorf("body = %q, want the origin's response", body)
	}
}

// TestRelayFailsClosedOnWrongCredentials checks a bad password surfaces as a
// failed request rather than silently falling back to a direct connection —
// which would leak the real IP, the worst possible failure for this tool.
func TestRelayFailsClosedOnWrongCredentials(t *testing.T) {
	t.Parallel()

	origin := startOrigin(t)
	socksAddr := startSocks5(t, "kel", "s3cret")

	out, err := proxy.ParseSpec("socks5://kel:WRONG@" + socksAddr)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}

	relay, err := proxy.Start(t.Context(), out)
	if err != nil {
		t.Fatalf("proxy.Start: %v", err)
	}
	defer func() { _ = relay.Close() }()

	proxyURL, _ := url.Parse("http://" + relay.Addr())
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, origin, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	resp, err := client.Do(req)
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode == http.StatusOK {
			t.Fatal("a wrong password must not yield a successful fetch")
		}
	}
}

// startOrigin serves a fixed body and returns its URL.
func startOrigin(t *testing.T) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("origin listen: %v", err)
	}

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "hello from origin")
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	return "http://" + ln.Addr().String()
}

// startSocks5 runs a SOCKS5 server that requires the given credentials.
func startSocks5(t *testing.T, user, pass string) string {
	t.Helper()

	server := socks5.NewServer(
		socks5.WithAuthMethods([]socks5.Authenticator{
			socks5.UserPassAuthenticator{Credentials: socks5.StaticCredentials{user: pass}},
		}),
	)

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("socks listen: %v", err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = ln.Close() })

	return ln.Addr().String()
}

// getThrough fetches target through the local HTTP proxy at proxyAddr.
func getThrough(t *testing.T, proxyAddr, target string) string {
	t.Helper()

	proxyURL, err := url.Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatalf("parsing proxy addr: %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   15 * time.Second,
	}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request through relay: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}

	return strings.TrimSpace(string(body))
}

// TestConcurrentRelaysAreRaceFree covers the TUI's whole reason for existing:
// several profiles open at once, so several relays starting at once. sing-box's
// include.Context assigns package-level function variables while building its
// registries, with no synchronization of its own, so overlapping starts race —
// caught only under -race, and silently corrupting otherwise.
func TestConcurrentRelaysAreRaceFree(t *testing.T) {
	t.Parallel()

	const relays = 4

	origin := startOrigin(t)
	socksAddr := startSocks5(t, "kel", "s3cret")

	out, err := proxy.ParseSpec("socks5://kel:s3cret@" + socksAddr)
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}

	var wg sync.WaitGroup
	for i := range relays {
		wg.Go(func() {
			relay, err := proxy.Start(t.Context(), out)
			if err != nil {
				t.Errorf("relay %d: Start: %v", i, err)

				return
			}
			defer func() { _ = relay.Close() }()

			if body := getThrough(t, relay.Addr(), origin); body != "hello from origin" {
				t.Errorf("relay %d: body = %q", i, body)
			}
		})
	}
	wg.Wait()
}

// TestParseSpecHandlesSocks4 keeps a capability the old hand-rolled relay had.
// sing-box's socks outbound takes a version, so socks4 still works — but
// credentials on it do not, because socks4 authenticates a bare userid with no
// password method at all. Dropping them silently would fail the connection for
// a reason nobody could see.
func TestParseSpecHandlesSocks4(t *testing.T) {
	t.Parallel()

	out, err := proxy.ParseSpec("socks4://1.2.3.4:1080")
	if err != nil {
		t.Fatalf("ParseSpec: %v", err)
	}
	if out[keyKind] != "socks" || out["version"] != "4" {
		t.Errorf("socks4 mapped to %#v", out)
	}

	if _, err := proxy.ParseSpec("socks4://user:pass@1.2.3.4:1080"); err == nil {
		t.Error("socks4 with credentials must be rejected; the protocol cannot carry them")
	}
}
