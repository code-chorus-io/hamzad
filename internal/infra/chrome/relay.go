package chrome

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// errRelayScheme is returned when an authenticated proxy uses a scheme the relay
// cannot dial upstream.
var errRelayScheme = errors.New("proxy relay: unsupported upstream scheme")

// errUpstreamRefused is returned when the upstream HTTP proxy rejects the
// CONNECT tunnel (e.g. bad credentials); the response status is wrapped onto it.
var errUpstreamRefused = errors.New("proxy relay: upstream refused CONNECT")

// dialTimeout bounds how long the relay waits to establish an upstream
// connection (both the TCP dial and any proxy handshake).
const dialTimeout = 30 * time.Second

// authRelay is a loopback HTTP proxy that bridges Chrome to an upstream proxy
// requiring authentication. Chrome cannot supply proxy credentials from the
// command line, and cannot authenticate SOCKS proxies at all; so for an
// authenticated profile we point Chrome at this relay (unauthenticated) and let
// it perform the upstream handshake — including SOCKS5 user/pass. No CDP session
// is involved, so this works on the clean launch path as well as under --cdp-port.
type authRelay struct {
	ln     net.Listener
	dialer xproxy.Dialer
}

// startAuthRelay builds an upstream dialer from a credentialed proxy URL and
// begins serving on a random loopback port. Close stops it.
func startAuthRelay(ctx context.Context, u *url.URL) (*authRelay, error) {
	dialer, err := upstreamDialer(u)
	if err != nil {
		return nil, err
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting proxy relay: %w", err)
	}

	r := &authRelay{ln: ln, dialer: dialer}
	go r.serve()

	return r, nil
}

// Addr is the host:port Chrome should point --proxy-server at (unauthenticated).
func (r *authRelay) Addr() string { return r.ln.Addr().String() }

// Close stops accepting new connections; in-flight tunnels drain on their own.
func (r *authRelay) Close() error {
	if err := r.ln.Close(); err != nil {
		return fmt.Errorf("closing proxy relay: %w", err)
	}

	return nil
}

func (r *authRelay) serve() {
	for {
		conn, err := r.ln.Accept()
		if err != nil {
			return // listener closed
		}

		go r.handle(conn)
	}
}

// handle bridges a single Chrome connection to the upstream proxy. It supports
// the two request shapes Chrome sends to an HTTP proxy: CONNECT (for HTTPS and
// any tunneled traffic) and absolute-form requests (for plain http:// URLs).
func (r *authRelay) handle(client net.Conn) {
	defer func() { _ = client.Close() }()

	br := bufio.NewReader(client)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	isConnect := req.Method == http.MethodConnect
	target := req.Host
	if !isConnect {
		target = req.URL.Host
	}
	target = ensurePort(target, isConnect)

	up, err := r.dialer.Dial("tcp", target)
	if err != nil {
		if isConnect {
			_, _ = io.WriteString(client, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		}

		return
	}
	defer func() { _ = up.Close() }()

	if isConnect {
		if _, err := io.WriteString(client, "HTTP/1.1 200 Connection established\r\n\r\n"); err != nil {
			return
		}
	} else {
		// Rewrite the buffered request into origin-form and replay it to the
		// origin server reached through the upstream proxy.
		req.URL.Scheme = ""
		req.URL.Host = ""
		req.RequestURI = ""
		if err := req.Write(up); err != nil {
			return
		}
	}

	splice(up, client, br)
}

// splice copies bytes both ways between the upstream connection and the client,
// reading the client side through br (which may already hold buffered bytes from
// the initial request parse). It returns once either direction closes.
func splice(up, client net.Conn, clientR io.Reader) {
	done := make(chan struct{}, 2)

	cp := func(dst net.Conn, src io.Reader) {
		_, _ = io.Copy(dst, src)
		// Half-close so the paired copy sees EOF and unwinds.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		done <- struct{}{}
	}

	go cp(up, clientR)
	go cp(client, up)

	<-done
	<-done
}

// upstreamDialer builds a dialer that reaches a target host through the given
// authenticated proxy, performing whatever handshake the scheme requires.
func upstreamDialer(u *url.URL) (xproxy.Dialer, error) {
	switch u.Scheme {
	case "socks5":
		var auth *xproxy.Auth
		if u.User != nil {
			pass, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: pass}
		}

		d, err := xproxy.SOCKS5("tcp", u.Host, auth, xproxy.Direct)
		if err != nil {
			return nil, fmt.Errorf("proxy relay: building socks5 dialer: %w", err)
		}

		return d, nil
	case "http", "https":
		return &httpConnectDialer{proxy: u}, nil
	default:
		return nil, fmt.Errorf("%w: %q", errRelayScheme, u.Scheme)
	}
}

// httpConnectDialer reaches a target by issuing an authenticated CONNECT to an
// HTTP(S) proxy, tunneling all traffic (including plain http, which the relay
// forwards through the tunnel in origin-form) through the resulting connection.
type httpConnectDialer struct {
	proxy *url.URL
}

// Dial reaches target through the HTTP(S) proxy by opening an authenticated
// CONNECT tunnel and returning the resulting connection.
func (d *httpConnectDialer) Dial(_, target string) (net.Conn, error) {
	conn, err := (&net.Dialer{Timeout: dialTimeout}).DialContext(context.Background(), "tcp", d.proxy.Host)
	if err != nil {
		return nil, fmt.Errorf("proxy relay: dialing upstream: %w", err)
	}

	if d.proxy.Scheme == "https" {
		tconn := tls.Client(conn, &tls.Config{ServerName: hostOnly(d.proxy.Host), MinVersion: tls.VersionTLS12})
		if err := tconn.HandshakeContext(context.Background()); err != nil {
			_ = conn.Close()

			return nil, fmt.Errorf("proxy relay: upstream TLS handshake: %w", err)
		}
		conn = tconn
	}

	if err := d.connect(conn, target); err != nil {
		_ = conn.Close()

		return nil, err
	}

	return conn, nil
}

// connect sends the CONNECT request (with Basic auth when the proxy carries
// credentials) and verifies the upstream accepted the tunnel.
func (d *httpConnectDialer) connect(conn net.Conn, target string) error {
	req := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n"
	if d.proxy.User != nil {
		pass, _ := d.proxy.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(d.proxy.User.Username() + ":" + pass))
		req += "Proxy-Authorization: Basic " + cred + "\r\n"
	}
	req += "\r\n"

	if _, err := io.WriteString(conn, req); err != nil {
		return fmt.Errorf("proxy relay: writing CONNECT: %w", err)
	}

	// After a 200 the upstream sends nothing until the client speaks, so the
	// bufio.Reader will not over-read tunneled bytes.
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return fmt.Errorf("proxy relay: reading CONNECT response: %w", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: %s", errUpstreamRefused, resp.Status)
	}

	return nil
}

// ensurePort appends the default port for the scheme when the host omits one:
// 443 for a CONNECT tunnel, 80 for a forwarded plain-http request.
func ensurePort(hostport string, isConnect bool) string {
	if _, _, err := net.SplitHostPort(hostport); err == nil {
		return hostport
	}

	if isConnect {
		return net.JoinHostPort(hostport, "443")
	}

	return net.JoinHostPort(hostport, "80")
}

// hostOnly strips a :port suffix, returning the bare host for TLS SNI.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}

	return hostport
}
