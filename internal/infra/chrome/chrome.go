// Package chrome launches a Chrome/Chromium instance for a profile and applies
// its proxy, timezone, geolocation, and fingerprint overrides over the Chrome
// DevTools Protocol. It drives an off-the-shelf Chromium — no patched browser —
// so spoofing is best-effort via CDP and injected page scripts.
package chrome

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/1995parham/koochooloologin/internal/domain/profile"
)

// ErrNoBrowser is returned when no Chrome/Chromium executable can be located.
var ErrNoBrowser = errors.New(
	"no chrome or chromium executable found; run 'browser install' to fetch one, or set chrome_path")

// Options configures a single browser launch.
type Options struct {
	// ExecPath is the browser binary. When empty, Detect is used.
	ExecPath string
	// UserDataDir is the profile's isolated Chrome data directory.
	UserDataDir string
	// Proxy, when non-nil, routes traffic through the given proxy; credentials
	// in its userinfo are supplied to Chrome's proxy auth challenge.
	Proxy *url.URL
	// Timezone is an IANA zone applied via Emulation.setTimezoneOverride.
	Timezone string
	// StartURL is the first page to open; defaults to about:blank.
	StartURL string
	// DebugPort, when > 0, fixes Chrome's remote-debugging port so external
	// automation (Puppeteer/Playwright/Selenium) can attach over CDP.
	DebugPort int
	// Fingerprint carries the per-profile spoofing parameters.
	Fingerprint profile.Fingerprint
}

// Detect returns the first Chrome/Chromium binary found on PATH across the
// common executable names, or an empty string when none is present.
func Detect() string {
	names := []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"}

	switch runtime.GOOS {
	case "darwin":
		names = append(names,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		)
	case "windows":
		// Chrome installs outside PATH on Windows, so the bare names above never
		// match and the well-known locations are the only ones that will.
		names = append(names,
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			filepath.Join(os.Getenv("LOCALAPPDATA"), `Google\Chrome\Application\chrome.exe`),
		)
	}

	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}

	return ""
}

// Launch starts the browser and applies the profile overrides, then blocks
// until the browser is closed or ctx is cancelled.
//
// By default it uses the clean path (launchClean): an ordinary Chrome child
// process with no CDP session and no remote-debugging port, so none of the
// automation tells that block sites like Google sign-in are present. When a
// fixed debug port is requested (o.DebugPort > 0, i.e. `--cdp-port`), it uses
// the CDP path (launchCDP) instead, which additionally applies the geolocation
// and injected fingerprint patches that require a live DevTools session.
func Launch(ctx context.Context, o Options) error {
	execPath, err := Resolve(o.ExecPath, "")
	if err != nil {
		return err
	}

	// Chrome is force-killed (SIGKILL) when ctx is cancelled — which is how the
	// TUI closes a session and how Ctrl-C ends the CLI — so it never runs its own
	// cleanup and leaves the ProcessSingleton lock files behind. On the next
	// launch of the same profile Chrome would see them, assume another browser
	// owns the user-data-dir, hand off, and exit ("Opening in existing browser
	// session."). Only one browser ever owns a profile dir at a time, so any lock
	// we find here is stale; clear it before launching.
	clearSingletonLocks(o.UserDataDir)

	// Resolve the --proxy-server argument, starting a local auth relay when the
	// profile's proxy carries credentials (Chrome cannot authenticate proxies
	// itself). The relay is independent of CDP, so both launch paths use it.
	proxyArg, closeRelay, err := proxyForLaunch(ctx, o)
	if err != nil {
		return err
	}
	if closeRelay != nil {
		defer func() { _ = closeRelay() }()
	}

	if o.DebugPort > 0 {
		return launchCDP(ctx, o, execPath, proxyArg)
	}

	return launchClean(ctx, o, execPath, proxyArg)
}

// launchCDP drives Chrome over the DevTools Protocol via chromedp, applying the
// full override stack (including geolocation and the injected fingerprint
// patches). It opens a remote-debugging port, so it is used only when the caller
// explicitly asks for one with --cdp-port for automation to attach.
func launchCDP(ctx context.Context, o Options, execPath, proxyArg string) error {
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, execOptions(o, execPath, proxyArg)...)
	defer cancelAlloc()

	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if err := chromedp.Run(taskCtx, buildActions(o)...); err != nil {
		return fmt.Errorf("launching chrome: %w", err)
	}

	// Block until the browser window is closed or the caller cancels.
	<-taskCtx.Done()

	return nil
}

// launchClean starts Chrome as an ordinary child process with only command-line
// and environment overrides — no CDP session, no remote-debugging port, and thus
// none of the automation tells (an open debug port, the Runtime.enable leak)
// that sites use to block a controlled browser. A plain Chrome even reports
// navigator.webdriver === false natively, with no patch needed.
//
// The tradeoff: overrides that require a live CDP session (platform,
// accept-language, geolocation, screen metrics) or an injected page script (the
// canvas/WebGL/JS fingerprint patches) do not apply here — see CleanModeDropped,
// which the callers surface as a heads-up. Timezone still applies via the TZ
// environment variable, and user-agent, proxy, window size, and the primary
// language via flags.
func launchClean(ctx context.Context, o Options, execPath, proxyArg string) error {
	// execPath is the operator-configured browser binary and the args are derived
	// from the user's own profile, so this is not untrusted input.
	cmd := exec.CommandContext(ctx, execPath, cleanArgs(o, proxyArg)...) //nolint:gosec // G204: operator-controlled browser + own profile
	cmd.Env = os.Environ()
	if o.Timezone != "" {
		cmd.Env = append(cmd.Env, "TZ="+o.Timezone)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting chrome: %w", err)
	}

	err := cmd.Wait()
	// A ctx cancellation (Ctrl-C or the TUI closing the session) kills Chrome,
	// which surfaces here as a signal-kill error; that is the normal way a
	// session ends, not a failure.
	if ctx.Err() != nil {
		return nil //nolint:nilerr // ctx-cancel kill is a normal session end, not an error
	}
	if err != nil {
		return fmt.Errorf("chrome exited: %w", err)
	}

	return nil
}

// cleanArgs assembles the minimal, human-looking flag set for a clean launch.
// It deliberately omits the automation-flavored flags chromedp adds by default.
func cleanArgs(o Options, proxyArg string) []string {
	args := []string{
		"--user-data-dir=" + o.UserDataDir,
		"--no-first-run",
		"--no-default-browser-check",
	}
	if proxyArg != "" {
		args = append(args, "--proxy-server="+proxyArg)
	}
	if ua := o.Fingerprint.UserAgent; ua != "" {
		args = append(args, "--user-agent="+ua)
	}
	if w, h := o.Fingerprint.ScreenWidth, o.Fingerprint.ScreenHeight; w > 0 && h > 0 {
		args = append(args, fmt.Sprintf("--window-size=%d,%d", w, h))
	}
	if langs := o.Fingerprint.Languages; len(langs) > 0 {
		args = append(args, "--lang="+langs[0])
	}
	// A command-line flag, so unlike the CDP overrides this one survives the
	// clean launch — which matters, because it is the leak that matters most.
	if policy := webRTCPolicy(o); policy != "" {
		args = append(args, "--webrtc-ip-handling-policy="+policy)
	}

	return append(args, startURL(o))
}

// proxyForLaunch resolves the --proxy-server value for Chrome. An unauthenticated
// proxy is passed straight through; an authenticated one is fronted by a local
// relay (returned as a closer to stop on exit) that performs the upstream
// handshake, since Chrome cannot supply proxy credentials. Returns an empty arg
// and nil closer when the profile has no proxy.
func proxyForLaunch(ctx context.Context, o Options) (string, func() error, error) {
	if o.Proxy == nil {
		return "", nil, nil
	}
	if o.Proxy.User == nil {
		return proxyServerArg(o.Proxy), nil, nil
	}

	relay, err := startAuthRelay(ctx, o.Proxy)
	if err != nil {
		return "", nil, err
	}

	return "http://" + relay.Addr(), relay.Close, nil
}

// clearSingletonLocks removes Chrome's ProcessSingleton files from a profile's
// user-data-dir. They are symlinks (SingletonLock -> host-pid, plus the socket
// and cookie); a browser that exits cleanly deletes them, but one killed with
// SIGKILL cannot, orphaning them. It is a no-op for an empty dir (chromedp then
// uses a fresh temp dir) or when the files are absent.
func clearSingletonLocks(dir string) {
	if dir == "" {
		return
	}

	for _, name := range []string{"SingletonLock", "SingletonSocket", "SingletonCookie"} {
		// Remove the symlink itself, not its target. A missing file is fine, and
		// a lingering lock is not fatal (Chrome may still break it), so any error
		// is deliberately ignored rather than aborting the launch.
		_ = os.Remove(filepath.Join(dir, name))
	}
}

// execOptions assembles the process-level launch flags for the browser. proxyArg
// is the pre-resolved --proxy-server value (possibly a local auth relay), empty
// when the profile has no proxy.
func execOptions(o Options, execPath, proxyArg string) []chromedp.ExecAllocatorOption {
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		chromedp.ExecPath(execPath),
		chromedp.Flag("headless", false),
		chromedp.UserDataDir(o.UserDataDir),
		// Drop the "controlled by automated software" infobar chromedp sets by
		// default. navigator.webdriver is neutralized in JS instead (see
		// webdriverPatch) to avoid the "unsupported command-line flag" banner
		// that --disable-blink-features would raise.
		chromedp.Flag("enable-automation", false),
		// chromedp mutes audio by default; a real browsing session should play it.
		chromedp.Flag("mute-audio", false),
	)
	if ua := o.Fingerprint.UserAgent; ua != "" {
		opts = append(opts, chromedp.UserAgent(ua))
	}
	if proxyArg != "" {
		opts = append(opts, chromedp.ProxyServer(proxyArg))
	}
	if w, h := o.Fingerprint.ScreenWidth, o.Fingerprint.ScreenHeight; w > 0 && h > 0 {
		opts = append(opts, chromedp.WindowSize(w, h))
	}
	if langs := o.Fingerprint.Languages; len(langs) > 0 {
		opts = append(opts, chromedp.Flag("lang", langs[0]))
	}
	if policy := webRTCPolicy(o); policy != "" {
		opts = append(opts, chromedp.Flag("webrtc-ip-handling-policy", policy))
	}
	if o.DebugPort > 0 {
		opts = append(opts, chromedp.Flag("remote-debugging-port", strconv.Itoa(o.DebugPort)))
	}

	return opts
}

// proxyServerArg renders the proxy for Chrome's --proxy-server flag, which
// understands scheme://host:port but ignores any embedded credentials.
func proxyServerArg(u *url.URL) string {
	return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
}

// buildActions assembles the CDP actions that apply the profile overrides
// before the first navigation. Proxy authentication is handled out-of-band by
// the local relay (see proxyForLaunch), so no Fetch auth interception is needed.
func buildActions(o Options) []chromedp.Action {
	var acts []chromedp.Action

	acts = append(acts, identityActions(o.Fingerprint)...)
	acts = append(acts, environmentActions(o)...)

	if script := patchScript(o.Fingerprint); script != "" {
		acts = append(acts, chromedp.ActionFunc(func(ctx context.Context) error {
			if _, err := page.AddScriptToEvaluateOnNewDocument(script).Do(ctx); err != nil {
				return fmt.Errorf("installing fingerprint patch: %w", err)
			}

			return nil
		}))
	}

	return append(acts, chromedp.Navigate(startURL(o)))
}

// identityActions applies the who-am-I overrides: user-agent, its UA-CH
// platform/language hints, and the JS locale.
func identityActions(fp profile.Fingerprint) []chromedp.Action {
	var acts []chromedp.Action

	if fp.UserAgent != "" {
		ua := emulation.SetUserAgentOverride(fp.UserAgent)
		if fp.AcceptLanguage != "" {
			ua = ua.WithAcceptLanguage(fp.AcceptLanguage)
		}
		if fp.Platform != "" {
			ua = ua.WithPlatform(fp.Platform)
		}
		acts = append(acts, ua)
	}

	if len(fp.Languages) > 0 {
		acts = append(acts, emulation.SetLocaleOverride().WithLocale(fp.Languages[0]))
	}

	return acts
}

// environmentActions applies the where-am-I overrides: timezone, geolocation
// (with its permission grant), and screen metrics.
func environmentActions(o Options) []chromedp.Action {
	var acts []chromedp.Action

	if o.Timezone != "" {
		acts = append(acts, emulation.SetTimezoneOverride(o.Timezone))
	}

	if g := o.Fingerprint.Geolocation; g != nil {
		acc := g.Accuracy
		if acc == 0 {
			acc = 100
		}
		// Grant the permission first so getCurrentPosition resolves instead of
		// prompting, then install the coordinate override.
		acts = append(acts,
			browser.SetPermission(&browser.PermissionDescriptor{Name: geolocationKey}, browser.PermissionSettingGranted),
			emulation.SetGeolocationOverride().
				WithLatitude(g.Latitude).
				WithLongitude(g.Longitude).
				WithAccuracy(acc),
		)
	}

	if w, h := o.Fingerprint.ScreenWidth, o.Fingerprint.ScreenHeight; w > 0 && h > 0 {
		acts = append(acts, emulation.SetDeviceMetricsOverride(int64(w), int64(h), 1, false))
	}

	return acts
}
