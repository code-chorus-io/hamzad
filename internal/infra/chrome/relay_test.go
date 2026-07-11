package chrome //nolint:testpackage // white-box: drives the unexported relay directly

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

// TestAuthRelayTunnelsWithInjectedCredentials proves the end-to-end path a
// credentialed profile takes: Chrome (here, an http.Client) → the local relay →
// an authenticated upstream proxy → the target. The upstream demands a specific
// Proxy-Authorization, so a successful fetch confirms the relay injected the
// profile's credentials that Chrome itself cannot supply.
func TestAuthRelayTunnelsWithInjectedCredentials(t *testing.T) {
	t.Parallel()

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello through the tunnel")
	}))
	defer target.Close()

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	var proxied atomic.Bool
	upstreamAddr := fakeAuthProxy(t, wantAuth, &proxied)

	relay, err := startAuthRelay(t.Context(), mustURL(t, "http://user:pass@"+upstreamAddr))
	if err != nil {
		t.Fatalf("startAuthRelay: %v", err)
	}
	defer func() { _ = relay.Close() }()

	pool := x509.NewCertPool()
	pool.AddCert(target.Certificate())
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(mustURL(t, "http://"+relay.Addr())),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	resp, err := doGet(t, client, target.URL)
	if err != nil {
		t.Fatalf("GET through relay: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello through the tunnel" {
		t.Errorf("body = %q, want the tunneled greeting", body)
	}
	if !proxied.Load() {
		t.Error("upstream proxy was never traversed — relay did not route through it")
	}
}

// TestAuthRelayRejectsWrongCredentials confirms the upstream's auth is actually
// enforced end-to-end: a relay built with the wrong password fails to tunnel.
func TestAuthRelayRejectsWrongCredentials(t *testing.T) {
	t.Parallel()

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "unreachable")
	}))
	defer target.Close()

	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	var proxied atomic.Bool
	upstreamAddr := fakeAuthProxy(t, wantAuth, &proxied)

	relay, err := startAuthRelay(t.Context(), mustURL(t, "http://user:wrong@"+upstreamAddr))
	if err != nil {
		t.Fatalf("startAuthRelay: %v", err)
	}
	defer func() { _ = relay.Close() }()

	pool := x509.NewCertPool()
	pool.AddCert(target.Certificate())
	client := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(mustURL(t, "http://"+relay.Addr())),
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
	}}

	resp, err := doGet(t, client, target.URL)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("expected the fetch to fail when the upstream rejects the credentials")
	}
}

func TestEnsurePort(t *testing.T) {
	t.Parallel()

	cases := []struct {
		host      string
		isConnect bool
		want      string
	}{
		{"example.com", true, "example.com:443"},
		{"example.com", false, "example.com:80"},
		{"example.com:8443", true, "example.com:8443"},
		{"1.2.3.4:1080", false, "1.2.3.4:1080"},
	}
	for _, tc := range cases {
		if got := ensurePort(tc.host, tc.isConnect); got != tc.want {
			t.Errorf("ensurePort(%q, %v) = %q, want %q", tc.host, tc.isConnect, got, tc.want)
		}
	}
}

// fakeAuthProxy is a minimal CONNECT proxy that requires an exact
// Proxy-Authorization header, tunneling to the requested target only when it
// matches. It flips proxied once a request reaches it.
func fakeAuthProxy(t *testing.T, wantAuth string, proxied *atomic.Bool) string {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake proxy listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}

			go serveFakeProxy(conn, wantAuth, proxied)
		}
	}()

	return ln.Addr().String()
}

func serveFakeProxy(client net.Conn, wantAuth string, proxied *atomic.Bool) {
	defer func() { _ = client.Close() }()

	proxied.Store(true)

	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(client, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")

		return
	}
	if req.Header.Get("Proxy-Authorization") != wantAuth {
		_, _ = io.WriteString(client, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")

		return
	}

	up, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", req.Host)
	if err != nil {
		_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")

		return
	}
	defer func() { _ = up.Close() }()

	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
		return
	}

	go func() { _, _ = io.Copy(up, br) }()
	_, _ = io.Copy(client, up)
}

// doGet issues a context-bound GET through the client, centralizing the request
// construction the linter requires (no bare client.Get).
func doGet(t *testing.T, c *http.Client, rawURL string) (*http.Response, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}

	return c.Do(req) //nolint:wrapcheck // test helper: caller inspects the raw error
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}

	return u
}
