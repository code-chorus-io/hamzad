package chrome //nolint:testpackage // white-box: exercises unexported confirm helpers

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
)

// TestExpectedConfigOmitsUnsetFields checks that a field the profile does not
// pin is left out of the expected map (rendered informational on the page)
// rather than showing a misleading zero value.
func TestExpectedConfigOmitsUnsetFields(t *testing.T) {
	t.Parallel()

	exp := expectedConfig(Options{Timezone: testTZ})

	if got := exp["timezone"]; got != testTZ {
		t.Errorf("timezone = %q, want Europe/Berlin", got)
	}
	// webdriver is always expected false because the launcher neutralizes it.
	if got := exp["webdriver"]; got != "false" {
		t.Errorf("webdriver = %q, want false", got)
	}
	for _, key := range []string{"screen", hardwareConcurrencyKey, deviceMemoryKey, geolocationKey, "proxy"} {
		if v, ok := exp[key]; ok && v != "" {
			t.Errorf("unset field %q leaked value %q", key, v)
		}
	}
}

// TestExpectedConfigFormatsSetFields checks the display strings for the pinned
// fields, including screen dimensions and geolocation precision.
func TestExpectedConfigFormatsSetFields(t *testing.T) {
	t.Parallel()

	exp := expectedConfig(Options{
		Fingerprint: profile.Fingerprint{
			ScreenWidth:        1920,
			ScreenHeight:       1080,
			HardwareConcurrent: 8,
			DeviceMemory:       16,
			Geolocation:        &profile.LatLng{Latitude: 52.520008, Longitude: 13.404954},
		},
	})

	cases := map[string]string{
		"screen":              "1920 × 1080",
		"hardwareConcurrency": "8",
		"deviceMemory":        "16",
		geolocationKey:        "52.52001, 13.40495",
	}
	for key, want := range cases {
		if got := exp[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestExpectedConfigProxyHidesCredentials ensures the proxy row never carries
// the userinfo from the proxy URL onto the page.
func TestExpectedConfigProxyHidesCredentials(t *testing.T) {
	t.Parallel()

	//nolint:gosec // fixture credentials, the point of the test is that they are hidden
	exp := expectedConfig(Options{ProxySpec: "http://alice:s3cret@proxy.example:8080"})

	if got := exp["proxy"]; strings.Contains(got, "alice") || strings.Contains(got, "s3cret") {
		t.Errorf("proxy field %q must not contain credentials", got)
	}
	if got := exp["proxy"]; !strings.Contains(got, "proxy.example:8080") {
		t.Errorf("proxy field %q should name the host", got)
	}
}

// TestConfirmPageURLEmbedsExpected checks the data: URL decodes to HTML with the
// expected JSON injected in place of the placeholder.
func TestConfirmPageURLEmbedsExpected(t *testing.T) {
	t.Parallel()

	u := confirmPageURL(Options{Timezone: "Asia/Tehran"})

	const prefix = "data:text/html;charset=utf-8;base64,"
	if !strings.HasPrefix(u, prefix) {
		t.Fatalf("url does not start with the data prefix: %q", u[:min(40, len(u))])
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(u, prefix))
	if err != nil {
		t.Fatalf("decoding data url: %v", err)
	}
	html := string(decoded)

	if strings.Contains(html, expectedPlaceholder) {
		t.Error("placeholder was not replaced with the expected JSON")
	}
	if !strings.Contains(html, `"timezone":"Asia/Tehran"`) {
		t.Error("expected timezone not embedded in the page")
	}
	// The literal percent signs in the CSS must survive verbatim (no fmt verb
	// mangling).
	if !strings.Contains(html, "width: 100%") {
		t.Error("CSS percent literals were corrupted")
	}
}

// TestStartURLPrefersProfileURL checks that a pinned start URL wins over the
// confirmation page, and the confirmation page is the fallback otherwise.
func TestStartURLPrefersProfileURL(t *testing.T) {
	t.Parallel()

	if got := startURL(Options{StartURL: "https://example.com"}); got != "https://example.com" {
		t.Errorf("startURL = %q, want the pinned URL", got)
	}
	if got := startURL(Options{}); !strings.HasPrefix(got, "data:text/html") {
		t.Errorf("startURL fallback = %q, want the confirmation data URL", got[:min(40, len(got))])
	}
}
