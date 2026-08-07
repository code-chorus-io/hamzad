package chrome

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/cdp"
)

// Keys and method names repeated across the override set.
const (
	keyURL      = "url"
	keyName     = "name"
	methodSetUA = "Emulation.setUserAgentOverride"
)

// endpointTimeout bounds the wait for Chrome to publish its websocket URL.
const endpointTimeout = 30 * time.Second

// overrides assembles the CDP commands that dress a profile, in the order they
// must be applied: identity before environment, patches before navigation.
//
// Every command here is deliberate. The only domain enabled is Page, and only
// because addScriptToEvaluateOnNewDocument silently does nothing without it —
// it returns a script identifier and then never runs the script. Runtime,
// Network and Log stay off: each changes observable behaviour in the page, and
// Runtime.enable in particular is a signal anti-bot services look for.
func overrides(o Options) []command {
	fp := o.Fingerprint
	cmds := identityOverrides(fp)
	cmds = append(cmds, environmentOverrides(o)...)

	// The fingerprint patch must land in the page's own world: an isolated
	// world has its own globals, so a defineProperty there would be invisible
	// to the page scripts it is meant to fool.
	if script := patchScript(fp); script != "" {
		cmds = append(cmds,
			command{"Page.enable", nil},
			command{"Page.addScriptToEvaluateOnNewDocument", map[string]any{"source": script}})
	}

	return append(cmds, command{"Page.navigate", map[string]any{keyURL: startURL(o)}})
}

// identityOverrides say who the browser claims to be.
func identityOverrides(fp profile.Fingerprint) []command {
	var cmds []command

	if fp.UserAgent != "" {
		params := map[string]any{"userAgent": fp.UserAgent}
		if fp.AcceptLanguage != "" {
			params["acceptLanguage"] = fp.AcceptLanguage
		}
		if fp.Platform != "" {
			params["platform"] = fp.Platform
		}
		cmds = append(cmds, command{methodSetUA, params})
	}
	if len(fp.Languages) > 0 {
		cmds = append(cmds, command{"Emulation.setLocaleOverride", map[string]any{"locale": fp.Languages[0]}})
	}

	return cmds
}

// environmentOverrides say where it claims to be, and on what.
func environmentOverrides(o Options) []command {
	var cmds []command
	fp := o.Fingerprint

	if o.Timezone != "" {
		cmds = append(cmds, command{"Emulation.setTimezoneOverride", map[string]any{"timezoneId": o.Timezone}})
	}
	if g := fp.Geolocation; g != nil {
		acc := g.Accuracy
		if acc == 0 {
			acc = 100
		}
		cmds = append(cmds,
			command{"Browser.setPermission", map[string]any{
				"permission": map[string]any{keyName: geolocationKey},
				"setting":    "granted",
			}},
			command{"Emulation.setGeolocationOverride", map[string]any{
				"latitude": g.Latitude, "longitude": g.Longitude, "accuracy": acc,
			}})
	}
	if w, h := fp.ScreenWidth, fp.ScreenHeight; w > 0 && h > 0 {
		cmds = append(cmds, command{"Emulation.setDeviceMetricsOverride", map[string]any{
			"width": w, "height": h, "deviceScaleFactor": 1, "mobile": false,
		}})
	}
	// Engine-level, so it holds inside Workers where an injected patch cannot.
	if n := fp.HardwareConcurrent; n > 0 {
		cmds = append(cmds, command{"Emulation.setHardwareConcurrencyOverride",
			map[string]any{"hardwareConcurrency": n}})
	}

	return cmds
}

// command is one protocol call.
type command struct {
	method string
	params any
}

// launchCDP starts Chrome with a debugging port and drives it over the protocol
// directly, rather than through a general-purpose driver library.
//
// The browser is started the same way the clean path starts it — an ordinary
// child process — so the only difference between the two launches is the
// debugging port and the overrides that need it.
func launchCDP(ctx context.Context, o Options, execPath, proxyArg string) error {
	args := append(cleanArgs(o, proxyArg), fmt.Sprintf("--remote-debugging-port=%d", o.DebugPort))

	//nolint:gosec // G204: operator-controlled browser binary and the user's own profile
	cmd := exec.CommandContext(ctx, execPath, args...)
	cmd.Env = os.Environ()
	if o.Timezone != "" {
		cmd.Env = append(cmd.Env, "TZ="+o.Timezone)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting chrome: %w", err)
	}

	client, err := applyOverrides(ctx, o)
	if err != nil {
		return err
	}
	// Every Emulation.* override is scoped to the DevTools session that set it,
	// and the injected script is dropped with it: Chrome reverts the lot the
	// moment the connection closes. So the session is held open for as long as
	// the browser runs, not just long enough to send the commands.
	defer func() { _ = client.Close() }()

	err = cmd.Wait()
	// A ctx-cancel kill is how a session normally ends, not a failure.
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("chrome exited: %w", err)
	}

	return nil
}

// applyOverrides connects to the freshly started browser and dresses the page,
// returning the still-open client. The caller owns it and must keep it open for
// the browser's lifetime — closing it reverts every override.
func applyOverrides(ctx context.Context, o Options) (*cdp.Client, error) {
	wsURL, err := waitForEndpoint(ctx, o.DebugPort)
	if err != nil {
		return nil, err
	}

	client, err := cdp.Dial(ctx, wsURL)
	if err != nil {
		return nil, err
	}

	if err := client.AttachToPage(ctx); err != nil {
		_ = client.Close()

		return nil, err
	}

	if err := backfillUserAgent(ctx, client, &o); err != nil {
		_ = client.Close()

		return nil, err
	}

	for _, c := range overrides(o) {
		if _, err := client.Send(ctx, c.method, c.params); err != nil {
			_ = client.Close()

			return nil, err
		}
	}

	return client, nil
}

// backfillUserAgent fills in the user-agent a profile did not pin but still
// needs, because Emulation.setUserAgentOverride is the only way to set
// navigator.platform and Accept-Language and it requires a user-agent. Without
// this a profile that pins a platform and no user-agent silently gets neither,
// while the confirmation page goes on claiming the platform applied.
//
// The browser's own user-agent is used, so the override changes nothing the
// page can see beyond the platform and language it was asked for.
func backfillUserAgent(ctx context.Context, client *cdp.Client, o *Options) error {
	fp := &o.Fingerprint
	if !needsUserAgentBackfill(*fp) {
		return nil
	}

	raw, err := client.Send(ctx, "Browser.getVersion", nil)
	if err != nil {
		return err
	}

	var version struct {
		UserAgent string `json:"userAgent"`
	}
	if err := json.Unmarshal(raw, &version); err != nil {
		return fmt.Errorf("decoding browser version: %w", err)
	}
	fp.UserAgent = version.UserAgent

	return nil
}

// needsUserAgentBackfill reports whether a fingerprint pins something that only
// setUserAgentOverride can carry, but no user-agent to carry it on.
func needsUserAgentBackfill(fp profile.Fingerprint) bool {
	return fp.UserAgent == "" && (fp.Platform != "" || fp.AcceptLanguage != "")
}

// waitForEndpoint polls Chrome's /json/version until it publishes a websocket
// URL, which it only does once the browser is actually up.
func waitForEndpoint(ctx context.Context, port int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, endpointTimeout)
	defer cancel()

	endpoint := fmt.Sprintf("http://127.0.0.1:%d/json/version", port)
	client := &http.Client{Timeout: 2 * time.Second}

	for {
		if url := probeEndpoint(ctx, client, endpoint); url != "" {
			return url, nil
		}

		select {
		case <-ctx.Done():
			return "", fmt.Errorf("waiting for devtools endpoint: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func probeEndpoint(ctx context.Context, client *http.Client, endpoint string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	var body struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ""
	}

	return body.WebSocketDebuggerURL
}
