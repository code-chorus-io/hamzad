package chrome //nolint:testpackage // white-box: asserts the unexported clean-launch arg builder

import (
	"slices"
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
)

// Shared fixtures for the launch and override assertions.
const (
	testLang     = "de-DE"
	testPlatform = "Win32"
	testTZ       = "Europe/Berlin"
)

// TestCleanArgsNeverOpensDebugPort is the guardrail for the whole feature: the
// default (clean) launch must never pass a remote-debugging flag, since an open
// debug port is exactly the automation tell that blocks sites like Google
// sign-in. It also checks the flag-based overrides that survive without CDP.
func TestCleanArgsNeverOpensDebugPort(t *testing.T) {
	t.Parallel()

	o := Options{
		UserDataDir: "/tmp/profile-x",
		Fingerprint: profile.Fingerprint{
			UserAgent:    "Mozilla/5.0 Test",
			ScreenWidth:  1920,
			ScreenHeight: 1080,
			Languages:    []string{testLang, "en-US"},
		},
	}

	args := cleanArgs(o, "http://127.0.0.1:5000")

	for _, a := range args {
		if strings.Contains(a, "remote-debugging") {
			t.Fatalf("clean launch must never open a debug port, found %q in %v", a, args)
		}
	}

	want := []string{
		"--user-data-dir=/tmp/profile-x",
		"--proxy-server=http://127.0.0.1:5000",
		"--user-agent=Mozilla/5.0 Test",
		"--window-size=1920,1080",
		"--lang=de-DE",
	}
	for _, w := range want {
		if !slices.Contains(args, w) {
			t.Errorf("clean args missing %q; got %v", w, args)
		}
	}
}

// TestCleanModeDropped reports exactly the overrides a clean launch cannot apply,
// so the CLI notice stays accurate.
func TestCleanModeDropped(t *testing.T) {
	t.Parallel()

	if got := CleanModeDropped(Options{}); got != "" {
		t.Errorf("a bare profile drops nothing, got %q", got)
	}

	o := Options{Fingerprint: profile.Fingerprint{
		Platform:           testPlatform,
		AcceptLanguage:     testLang,
		ScreenWidth:        1920,
		ScreenHeight:       1080,
		HardwareConcurrent: 8,
		DeviceMemory:       16,
		Geolocation:        &profile.LatLng{Latitude: 1, Longitude: 2},
		CanvasNoise:        true,
		WebGLRenderer:      "Apple GPU",
	}}
	got := CleanModeDropped(o)
	for _, sub := range []string{
		platformKey, geolocationKey, screenMetricsLabel, acceptLanguageLabel,
		canvasNoiseLabel, webGLLabel, hardwareConcurrencyLabel, deviceMemoryLabel,
	} {
		if !strings.Contains(got, sub) {
			t.Errorf("expected %q in dropped list, got %q", sub, got)
		}
	}
}

// TestCleanModeDroppedNamesFlagBackedOverrides pins the three overrides that
// used to go missing in silence. Each needs a CDP session, but each also has a
// look-alike command-line flag that makes the omission easy to miss:
// --user-agent carries the OS story while navigator.platform keeps the host's,
// --lang covers only the primary language, and --window-size sizes the window
// while screen.width still reports the host monitor. An unwarned platform is the
// worst of the three: a Windows user-agent on a Linux navigator.platform is a
// sharper signal than not spoofing at all.
func TestCleanModeDroppedNamesFlagBackedOverrides(t *testing.T) {
	t.Parallel()

	for name, fp := range map[string]profile.Fingerprint{
		platformKey:         {Platform: testPlatform, UserAgent: "Mozilla/5.0 (Windows NT 10.0)"},
		acceptLanguageLabel: {AcceptLanguage: "de-DE,de;q=0.9", Languages: []string{testLang}},
		screenMetricsLabel:  {ScreenWidth: 1920, ScreenHeight: 1080},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if got := CleanModeDropped(Options{Fingerprint: fp}); !strings.Contains(got, name) {
				t.Errorf("clean launch drops %s but the notice says %q", name, got)
			}
		})
	}
}

// TestCleanModeDroppedIgnoresSurvivingOverrides is the counterweight: warning
// about an override that a clean launch does apply would train the user to
// ignore the notice.
func TestCleanModeDroppedIgnoresSurvivingOverrides(t *testing.T) {
	t.Parallel()

	o := Options{
		Timezone: "Asia/Tehran",
		Fingerprint: profile.Fingerprint{
			UserAgent: "Mozilla/5.0 Test",
			Languages: []string{testLang},
		},
	}

	if got := CleanModeDropped(o); got != "" {
		t.Errorf("timezone, user-agent and language survive a clean launch, got %q", got)
	}
}
