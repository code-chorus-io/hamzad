// Package chrome launches a Chrome/Chromium instance for a profile and applies
// its proxy, timezone, geolocation, and fingerprint overrides over the Chrome
// DevTools Protocol. It drives an off-the-shelf Chromium — no patched browser —
// so spoofing is best-effort via CDP and injected page scripts.
package chrome

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/proxy"
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
	// ProxySpec is the profile's proxy as the user wrote it: a share link
	// (socks5://, vless://, trojan://, ss://) or a raw sing-box outbound JSON
	// object. Empty means a direct connection. It is not a *url.URL because the
	// protocols that matter most cannot be expressed as one.
	ProxySpec string
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

	// Advisory: a profile that cannot get its bookmark bar is still perfectly
	// launchable, so this must never block the browser from opening.
	if err := seedBookmarks(o.UserDataDir); err != nil {
		fmt.Fprintf(os.Stderr, "note: could not seed bookmarks: %v\n", err)
	}

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
// It deliberately omits the automation-flavored flags a driver library would
// add by default (--enable-automation, --disable-extensions and friends), each
// of which is its own tell.
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

// proxyForLaunch brings up the local HTTP front end Chrome will use, returning
// its address and a closer, or an empty address when the profile is direct.
//
// Everything goes through sing-box, including plain authenticated SOCKS5 that
// the old hand-rolled relay handled. One path is worth more than a shortcut for
// the simple case: two implementations would mean two sets of behaviour to keep
// honest, and the simple case is not the one that breaks.
func proxyForLaunch(ctx context.Context, o Options) (string, func() error, error) {
	if o.ProxySpec == "" {
		return "", nil, nil
	}

	outbound, err := proxy.ParseSpec(o.ProxySpec)
	if err != nil {
		return "", nil, err
	}

	relay, err := proxy.Start(ctx, outbound)
	if err != nil {
		return "", nil, err
	}

	return "http://" + relay.Addr(), relay.Close, nil
}

// clearSingletonLocks removes Chrome's ProcessSingleton files from a profile's
// user-data-dir. They are symlinks (SingletonLock -> host-pid, plus the socket
// and cookie); a browser that exits cleanly deletes them, but one killed with
// SIGKILL cannot, orphaning them. It is a no-op for an empty dir or when the
// files are absent.
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
