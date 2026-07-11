package chrome //nolint:testpackage // white-box: asserts the unexported clean-launch arg builder

import (
	"slices"
	"strings"
	"testing"

	"github.com/1995parham/koochooloologin/internal/domain/profile"
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
			Languages:    []string{"de-DE", "en-US"},
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
		Geolocation:   &profile.LatLng{Latitude: 1, Longitude: 2},
		CanvasNoise:   true,
		WebGLRenderer: "Apple GPU",
	}}
	got := CleanModeDropped(o)
	for _, sub := range []string{"geolocation", "canvas-noise", "webgl"} {
		if !strings.Contains(got, sub) {
			t.Errorf("expected %q in dropped list, got %q", sub, got)
		}
	}
}
