package chrome

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/code-chorus-io/hamzad/internal/infra/cdp"
)

// Cookie export and import, for moving a signed-in session between machines.
//
// Chrome encrypts each cookie value in its Cookies database with a key the
// operating system holds for it — the login Keychain on macOS, libsecret or
// kwallet on Linux, DPAPI and app-bound encryption on Windows. The key differs
// on every machine, so a Cookies file copied across is unreadable there, and
// Chrome silently drops every cookie it cannot decrypt: the profile arrives
// logged out while its Local Storage, which is not encrypted, arrives intact.
//
// Rather than reimplement each platform's key scheme (and chase Chrome's schema
// changes), the browser does the crypto itself. A short-lived helper instance is
// started on the profile with a DevTools port; on save it reads every cookie out
// in plaintext with Storage.getCookies, on restore it writes them back with
// Storage.setCookies and Chrome encrypts them under this machine's key. GoLogin
// solves the same problem the other way round — a patched browser launched with
// a machine-independent key — which a stock Chrome cannot do on macOS or
// Windows.
//
// The helper never reaches the network and never shows a window. The user's
// own session is still the clean launch, with no DevTools port; the helper runs
// only before or after it, on a profile nothing else has open.

// cookiesFormat versions the exported document.
const cookiesFormat = 1

// helperTimeout bounds one helper run, start to exit.
const helperTimeout = 60 * time.Second

// helperQuitGrace is how long the helper gets to flush and exit after
// Browser.close before it is killed.
const helperQuitGrace = 15 * time.Second

// blackholeProxy is where the helper's traffic goes: a loopback port nothing
// listens on. The helper exists to touch the cookie store, and a Chrome left to
// itself phones home at start-up (component updates, safe browsing, variations)
// from the real IP — exactly what the profile's proxy is there to hide.
const blackholeProxy = "http://127.0.0.1:9"

// devToolsPortFile is where Chrome writes the port it picked for
// --remote-debugging-port=0, followed by the browser target's path.
const devToolsPortFile = "DevToolsActivePort"

// ErrHelperExited is returned when the helper browser exits before its DevTools
// endpoint comes up — most often because another browser already has the
// profile open and the helper handed off to it.
var ErrHelperExited = errors.New("helper browser exited before it was ready (is the profile open elsewhere?)")

// errCookiesFormat is returned for an exported document this build cannot read.
var errCookiesFormat = errors.New("unsupported cookies export format")

// cookiesDoc is the exported document stored in the session bundle.
type cookiesDoc struct {
	Format int `json:"format"`
	// Browser is the product string of the browser that exported the cookies,
	// for diagnosis only.
	Browser string `json:"browser,omitempty"`
	// Cookies are Storage.getCookies entries, verbatim.
	Cookies []json.RawMessage `json:"cookies"`
}

// ExportCookies reads every cookie of the profile in userDataDir through a
// helper browser and returns them as a JSON document for ImportCookies, along
// with how many there were. The profile must not be open in another browser.
func ExportCookies(ctx context.Context, execPath, userDataDir string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	h, err := startHelper(ctx, execPath, userDataDir)
	if err != nil {
		return nil, 0, err
	}
	defer h.close(ctx)

	doc := cookiesDoc{Format: cookiesFormat}

	if raw, err := h.client.Send(ctx, "Browser.getVersion", nil); err == nil {
		var v struct {
			Product string `json:"product"`
		}
		if json.Unmarshal(raw, &v) == nil {
			doc.Browser = v.Product
		}
	}

	raw, err := h.client.Send(ctx, "Storage.getCookies", nil)
	if err != nil {
		return nil, 0, err
	}
	var got struct {
		Cookies []json.RawMessage `json:"cookies"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return nil, 0, fmt.Errorf("decoding cookies: %w", err)
	}
	doc.Cookies = got.Cookies
	if doc.Cookies == nil {
		doc.Cookies = []json.RawMessage{}
	}

	out, err := json.Marshal(doc)
	if err != nil {
		return nil, 0, fmt.Errorf("encoding cookies: %w", err)
	}

	return out, len(doc.Cookies), nil
}

// ImportCookies replaces the cookie store of the profile in userDataDir with the
// cookies in data (an ExportCookies document), written through a helper browser
// so they are encrypted with this machine's key. It returns how many cookies
// were written and how many the browser refused.
//
// The existing Cookies database is deleted first: a store carried over from
// another machine is encrypted under a key this one does not have, and the
// export is the authoritative copy of it.
func ImportCookies(ctx context.Context, execPath, userDataDir string, data []byte) (int, int, error) {
	var doc cookiesDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return 0, 0, fmt.Errorf("decoding cookies export: %w", err)
	}
	if doc.Format != cookiesFormat {
		return 0, 0, fmt.Errorf("%w: %d", errCookiesFormat, doc.Format)
	}

	params := make([]map[string]any, 0, len(doc.Cookies))
	for _, raw := range doc.Cookies {
		var c exportedCookie
		if err := json.Unmarshal(raw, &c); err != nil {
			return 0, 0, fmt.Errorf("decoding cookie: %w", err)
		}
		params = append(params, c.param())
	}

	if err := removeCookieStores(userDataDir); err != nil {
		return 0, 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()

	h, err := startHelper(ctx, execPath, userDataDir)
	if err != nil {
		return 0, 0, err
	}
	defer h.close(ctx)

	return setCookies(ctx, h.client, params)
}

// setCookies writes params in one call, falling back to one call per cookie
// when the batch is refused: Storage.setCookies rejects the whole batch over a
// single cookie the browser will not accept, and one odd cookie must not cost
// the session every other one.
func setCookies(ctx context.Context, client *cdp.Client, params []map[string]any) (int, int, error) {
	if len(params) == 0 {
		return 0, 0, nil
	}

	if _, err := client.Send(ctx, "Storage.setCookies", map[string]any{"cookies": params}); err == nil {
		return len(params), 0, nil
	}

	written, refused := 0, 0
	for _, p := range params {
		if _, err := client.Send(ctx, "Storage.setCookies", map[string]any{"cookies": []any{p}}); err != nil {
			if ctx.Err() != nil {
				return written, refused, fmt.Errorf("writing cookies: %w", ctx.Err())
			}
			refused++

			continue
		}
		written++
	}

	return written, refused, nil
}

// exportedCookie is the part of a DevTools Network.Cookie that setCookies takes
// back. Output-only fields (size, session's twin flags, sameParty) are dropped.
type exportedCookie struct {
	Name         string          `json:"name"`
	Value        string          `json:"value"`
	Domain       string          `json:"domain"`
	Path         string          `json:"path"`
	Expires      float64         `json:"expires"`
	Session      bool            `json:"session"`
	Secure       bool            `json:"secure"`
	HTTPOnly     bool            `json:"httpOnly"`
	SameSite     string          `json:"sameSite"`
	Priority     string          `json:"priority"`
	SourceScheme string          `json:"sourceScheme"`
	SourcePort   int             `json:"sourcePort"`
	PartitionKey json.RawMessage `json:"partitionKey"`
}

// param converts an exported cookie into a Network.CookieParam.
//
// A host-only cookie is set by URL, not domain: getCookies reports it with a
// bare domain, and setCookies given that domain would create a domain cookie
// (".example.com") that also reaches every subdomain — a different cookie from
// the one that was exported.
func (c exportedCookie) param() map[string]any {
	p := map[string]any{
		keyName:    c.Name,
		"value":    c.Value,
		"path":     c.Path,
		"secure":   c.Secure,
		"httpOnly": c.HTTPOnly,
	}

	if strings.HasPrefix(c.Domain, ".") {
		p["domain"] = c.Domain
	} else {
		scheme := "http"
		if c.Secure {
			scheme = "https"
		}
		p[keyURL] = scheme + "://" + c.Domain + c.Path
	}

	c.addOptional(p)

	return p
}

// addOptional copies the attributes setCookies takes as optional into p, only
// when the export carried them: an empty sameSite or a zero port would not mean
// "default" to the browser, it would be refused or taken literally.
func (c exportedCookie) addOptional(p map[string]any) {
	if !c.Session && c.Expires > 0 {
		p["expires"] = c.Expires
	}
	if c.SameSite != "" {
		p["sameSite"] = c.SameSite
	}
	if c.Priority != "" {
		p["priority"] = c.Priority
	}
	if c.SourceScheme != "" {
		p["sourceScheme"] = c.SourceScheme
	}
	if c.SourcePort > 0 {
		p["sourcePort"] = c.SourcePort
	}
	if len(c.PartitionKey) > 0 && string(c.PartitionKey) != "null" {
		p["partitionKey"] = c.PartitionKey
	}
}

// removeCookieStores deletes the Cookies database of every profile directory
// under userDataDir, in both the current location (<profile>/Network) and the
// pre-96 one (<profile>), with their journals.
func removeCookieStores(userDataDir string) error {
	patterns := []string{
		filepath.Join(userDataDir, "*", "Network", "Cookies"),
		filepath.Join(userDataDir, "*", "Network", "Cookies-journal"),
		filepath.Join(userDataDir, "*", "Cookies"),
		filepath.Join(userDataDir, "*", "Cookies-journal"),
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return fmt.Errorf("finding cookie stores: %w", err)
		}
		for _, m := range matches {
			if err := os.Remove(m); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("removing %q: %w", m, err)
			}
		}
	}

	return nil
}

// helper is a running helper browser and its DevTools connection.
type helper struct {
	cmd    *exec.Cmd
	client *cdp.Client
	// exited is closed once the process has been reaped.
	exited   chan struct{}
	portFile string
}

// helperArgs is the helper's command line.
//
// --no-startup-window rather than --headless: headless Chrome may pick a
// different OS key store than the headed browser (on Linux it can fall back to
// the basic store), and then it could not read the cookies the user's session
// wrote. A headed browser without a window uses the same store the session does.
// The DevTools connection keeps that windowless browser alive.
func helperArgs(userDataDir string) []string {
	return []string{
		"--user-data-dir=" + userDataDir,
		"--remote-debugging-port=0",
		"--no-startup-window",
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--proxy-server=" + blackholeProxy,
		"--proxy-bypass-list=<-loopback>",
	}
}

// startHelper launches the helper browser on userDataDir and connects to it.
//
// The port is chosen by Chrome (--remote-debugging-port=0) and read back from
// DevToolsActivePort, so the helper never collides with a port in use.
func startHelper(ctx context.Context, execPath, userDataDir string) (*helper, error) {
	portFile := filepath.Join(userDataDir, devToolsPortFile)
	// A leftover from an earlier run would point at a browser that is gone.
	_ = os.Remove(portFile)

	cmd := exec.Command(execPath, helperArgs(userDataDir)...) //nolint:gosec,noctx // G204: operator-controlled browser on the user's own profile; lifetime is managed by close, not ctx
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting helper browser: %w", err)
	}

	h := &helper{cmd: cmd, exited: make(chan struct{}), portFile: portFile}
	go func() {
		_ = cmd.Wait()
		close(h.exited)
	}()

	wsURL, err := waitForPortFile(ctx, portFile, h.exited)
	if err != nil {
		h.kill()

		return nil, err
	}

	client, err := cdp.Dial(ctx, wsURL)
	if err != nil {
		h.kill()

		return nil, err
	}
	h.client = client

	return h, nil
}

// waitForPortFile polls for DevToolsActivePort until it names an endpoint, the
// browser exits, or ctx ends.
func waitForPortFile(ctx context.Context, portFile string, exited <-chan struct{}) (string, error) {
	for {
		if url := readPortFile(portFile); url != "" {
			return url, nil
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for helper browser: %w", ctx.Err())
		case <-exited:
			return "", ErrHelperExited
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// readPortFile parses DevToolsActivePort ("<port>\n/devtools/browser/<id>\n")
// into a websocket URL, or returns "" while the file is absent or incomplete.
func readPortFile(portFile string) string {
	f, err := os.Open(portFile) //nolint:gosec // inside the profile dir we were given
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	scan := bufio.NewScanner(f)
	var lines []string
	for scan.Scan() {
		if line := strings.TrimSpace(scan.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	if scan.Err() != nil {
		return ""
	}
	if len(lines) < 2 || !strings.HasPrefix(lines[1], "/devtools/browser/") {
		return ""
	}

	return "ws://127.0.0.1:" + lines[0] + lines[1]
}

// close asks the browser to quit — a graceful exit is what flushes the cookie
// store to disk — and waits for it, killing it only after helperQuitGrace.
func (h *helper) close(ctx context.Context) {
	// The reply often never comes: the browser closes the socket on its way out.
	_, _ = h.client.Send(ctx, "Browser.close", nil)
	_ = h.client.Close()

	select {
	case <-h.exited:
	case <-time.After(helperQuitGrace):
		h.kill()
	}

	_ = os.Remove(h.portFile)
}

// kill stops the browser outright and reaps it.
func (h *helper) kill() {
	_ = h.cmd.Process.Kill()
	<-h.exited
	_ = os.Remove(h.portFile)
}
