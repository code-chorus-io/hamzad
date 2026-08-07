package chrome

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/1995parham/koochooloologin/internal/infra/proxy"
)

// Signal keys shared between expectedConfig, CleanModeDropped, and the
// confirmation page's reader script, which looks each value up by the same name.
const (
	// geolocationKey doubles as the CDP permission name granted at launch.
	geolocationKey         = "geolocation"
	platformKey            = "platform"
	deviceMemoryKey        = "deviceMemory"
	hardwareConcurrencyKey = "hardwareConcurrency"
)

// Labels naming the remaining overrides a clean launch cannot apply. They are
// what CleanModeDropped renders, and are named so the tests pinning that notice
// assert against the same spelling rather than a re-typed copy.
const (
	screenMetricsLabel       = "screen-metrics"
	acceptLanguageLabel      = "accept-language"
	canvasNoiseLabel         = "canvas-noise"
	webGLLabel               = "webgl"
	hardwareConcurrencyLabel = "hardware-concurrency"
	deviceMemoryLabel        = "device-memory"
)

// confirmPageURL builds a self-contained confirmation page and returns it as a
// data: URL. The page lists every value the profile configured ("expected")
// beside the value the browser actually reports to page JavaScript ("actual"),
// so a launch can be visually confirmed: the CDP overrides and injected patches
// all take effect on this data: document too, so a mismatch means the spoof did
// not apply. It is used as the default landing page when a profile pins no
// start URL of its own.
func confirmPageURL(o Options) string {
	expected := expectedConfig(o)

	raw, err := json.Marshal(expected)
	if err != nil {
		// expected holds only plain strings, so marshaling cannot fail; fall back
		// to an empty object rather than an unusable page if it somehow does.
		raw = []byte("{}")
	}

	// A plain replace (not fmt.Sprintf) because the HTML/CSS contains literal
	// percent signs (e.g. width: 100%) that a format string would misread.
	html := strings.Replace(confirmHTML, expectedPlaceholder, string(raw), 1)

	return "data:text/html;charset=utf-8;base64," + base64.StdEncoding.EncodeToString([]byte(html))
}

// expectedConfig collects the profile's configured values as display strings,
// keyed to match the reader functions in the confirmation page's script. An
// empty value marks a field the profile did not pin, rendered as informational
// (no pass/fail) on the page.
func expectedConfig(o Options) map[string]string {
	fp := o.Fingerprint

	exp := map[string]string{
		"userAgent": fp.UserAgent,
		platformKey: fp.Platform,
		"timezone":  o.Timezone,
		"languages": strings.Join(fp.Languages, ", "),
		// The launcher always neutralizes the automation tell, so webdriver is
		// expected to read false on every profile.
		"webdriver":     "false",
		"webglVendor":   fp.WebGLVendor,
		"webglRenderer": fp.WebGLRenderer,
	}

	if fp.ScreenWidth > 0 && fp.ScreenHeight > 0 {
		exp["screen"] = fmt.Sprintf("%d × %d", fp.ScreenWidth, fp.ScreenHeight)
	}
	if fp.HardwareConcurrent > 0 {
		exp[hardwareConcurrencyKey] = strconv.Itoa(fp.HardwareConcurrent)
	}
	if fp.DeviceMemory > 0 {
		exp[deviceMemoryKey] = strconv.Itoa(fp.DeviceMemory)
	}
	if g := fp.Geolocation; g != nil {
		exp[geolocationKey] = fmt.Sprintf("%.5f, %.5f", g.Latitude, g.Longitude)
	}
	if o.ProxySpec != "" {
		// Describe redacts: the spec carries passwords, uuids and keys, and this
		// map is rendered into a page.
		exp["proxy"] = proxy.Describe(o.ProxySpec)
	}

	return exp
}

// CleanModeDropped names the profile overrides a no-CDP (clean) launch cannot
// apply, joined into a one-line heads-up, or "" when none are configured. These
// need either a live CDP session (platform, accept-language, geolocation, screen
// metrics) or an injected page script (the fingerprint patches), and take effect
// only under --cdp-port.
//
// Only timezone (via TZ), user-agent, proxy, window size, and the primary
// language survive as process flags or environment, so they are never listed.
// The list is ordered by how much a dropped signal gives the profile away.
// Platform leads: an OS preset sets navigator.platform and the user-agent
// together, and a clean launch applies only the user-agent — leaving a Windows
// UA on a host-platform navigator, which is a sharper tell than no spoof at all.
func CleanModeDropped(o Options) string {
	fp := o.Fingerprint

	// The first four have look-alike command-line flags that make them easy to
	// assume covered: --user-agent carries the OS story while navigator.platform
	// keeps the host's, --lang sets only the primary language, and --window-size
	// sizes the window while screen.width still reports the host monitor.
	signals := []struct {
		name   string
		pinned bool
	}{
		{platformKey, fp.Platform != ""},
		{geolocationKey, fp.Geolocation != nil},
		{screenMetricsLabel, fp.ScreenWidth > 0 && fp.ScreenHeight > 0},
		{acceptLanguageLabel, fp.AcceptLanguage != ""},
		{canvasNoiseLabel, fp.CanvasNoise},
		{webGLLabel, fp.WebGLVendor != "" || fp.WebGLRenderer != ""},
		{hardwareConcurrencyLabel, fp.HardwareConcurrent > 0},
		{deviceMemoryLabel, fp.DeviceMemory > 0},
	}

	var dropped []string
	for _, s := range signals {
		if s.pinned {
			dropped = append(dropped, s.name)
		}
	}

	// Join of an empty slice is "", which is the "nothing dropped" answer.
	return strings.Join(dropped, ", ")
}

// startURL returns the page to open first: the profile's own start URL when it
// pins one, otherwise the configuration confirmation page.
func startURL(o Options) string {
	if o.StartURL != "" {
		return o.StartURL
	}

	return confirmPageURL(o)
}
