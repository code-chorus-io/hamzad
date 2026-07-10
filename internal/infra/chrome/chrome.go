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
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"github.com/1995parham/koochooloologin/internal/domain/profile"
)

// ErrNoBrowser is returned when no Chrome/Chromium executable can be located.
var ErrNoBrowser = errors.New("no chrome or chromium executable found; set chrome_path")

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
	if runtime.GOOS == "darwin" {
		names = append(names,
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
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
func Launch(ctx context.Context, o Options) error {
	execPath := o.ExecPath
	if execPath == "" {
		execPath = Detect()
	}
	if execPath == "" {
		return ErrNoBrowser
	}

	// chromedp force-kills Chrome (SIGKILL) when ctx is cancelled — which is how
	// the TUI closes a session and how Ctrl-C ends the CLI — so Chrome never runs
	// its own cleanup and leaves the ProcessSingleton lock files behind. On the
	// next launch of the same profile Chrome would see them, assume another
	// browser owns the user-data-dir, hand off, and exit ("Opening in existing
	// browser session."). Only one browser ever owns a profile dir at a time, so
	// any lock we find here is stale; clear it before launching.
	clearSingletonLocks(o.UserDataDir)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, execOptions(o, execPath)...)
	defer cancelAlloc()

	taskCtx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	if o.Proxy != nil && o.Proxy.User != nil {
		user := o.Proxy.User.Username()
		pass, _ := o.Proxy.User.Password()
		listenProxyAuth(taskCtx, user, pass)
	}

	if err := chromedp.Run(taskCtx, buildActions(o)...); err != nil {
		return fmt.Errorf("launching chrome: %w", err)
	}

	// Block until the browser window is closed or the caller cancels.
	<-taskCtx.Done()

	return nil
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

// execOptions assembles the process-level launch flags for the browser.
func execOptions(o Options, execPath string) []chromedp.ExecAllocatorOption {
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
	if o.Proxy != nil {
		opts = append(opts, chromedp.ProxyServer(proxyServerArg(o.Proxy)))
	}
	if w, h := o.Fingerprint.ScreenWidth, o.Fingerprint.ScreenHeight; w > 0 && h > 0 {
		opts = append(opts, chromedp.WindowSize(w, h))
	}
	if langs := o.Fingerprint.Languages; len(langs) > 0 {
		opts = append(opts, chromedp.Flag("lang", langs[0]))
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

// listenProxyAuth answers proxy authentication challenges with the supplied
// credentials, since Chrome cannot take them from the --proxy-server URL.
func listenProxyAuth(ctx context.Context, user, pass string) {
	chromedp.ListenTarget(ctx, func(ev any) {
		switch e := ev.(type) {
		case *fetch.EventAuthRequired:
			go func() {
				_ = chromedp.Run(ctx, fetch.ContinueWithAuth(e.RequestID, &fetch.AuthChallengeResponse{
					Response: fetch.AuthChallengeResponseResponseProvideCredentials,
					Username: user,
					Password: pass,
				}))
			}()
		case *fetch.EventRequestPaused:
			go func() {
				_ = chromedp.Run(ctx, fetch.ContinueRequest(e.RequestID))
			}()
		}
	})
}

// buildActions assembles the CDP actions that apply the profile overrides
// before the first navigation.
func buildActions(o Options) []chromedp.Action {
	var acts []chromedp.Action

	// Enable request interception first so proxy auth challenges are caught.
	if o.Proxy != nil && o.Proxy.User != nil {
		acts = append(acts, fetch.Enable().WithHandleAuthRequests(true))
	}

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

	start := o.StartURL
	if start == "" {
		start = "about:blank"
	}

	return append(acts, chromedp.Navigate(start))
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
			browser.SetPermission(&browser.PermissionDescriptor{Name: "geolocation"}, browser.PermissionSettingGranted),
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
