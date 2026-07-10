package chrome

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// geolocationKey names the geolocation signal: it is both the CDP permission
// name granted at launch and the field key shared between expectedConfig and
// the confirmation page's reader script.
const geolocationKey = "geolocation"

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
		"platform":  fp.Platform,
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
		exp["hardwareConcurrency"] = strconv.Itoa(fp.HardwareConcurrent)
	}
	if fp.DeviceMemory > 0 {
		exp["deviceMemory"] = strconv.Itoa(fp.DeviceMemory)
	}
	if g := fp.Geolocation; g != nil {
		exp[geolocationKey] = fmt.Sprintf("%.5f, %.5f", g.Latitude, g.Longitude)
	}
	if o.Proxy != nil {
		exp["proxy"] = fmt.Sprintf("%s via %s", o.Proxy.Host, o.Proxy.Scheme)
	}

	return exp
}

// startURL returns the page to open first: the profile's own start URL when it
// pins one, otherwise the configuration confirmation page.
func startURL(o Options) string {
	if o.StartURL != "" {
		return o.StartURL
	}

	return confirmPageURL(o)
}
