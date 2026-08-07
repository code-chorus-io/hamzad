package chrome //nolint:testpackage // white-box: asserts the unexported CDP override builder

import (
	"slices"
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
)

// methods lists the command methods overrides() would send, in order.
func methods(o Options) []string {
	cmds := overrides(o)
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, c.method)
	}

	return out
}

// TestPatchScriptIsPrecededByPageEnable pins the fix for a silent failure that
// disabled every injected patch at once.
//
// Page.addScriptToEvaluateOnNewDocument does not error without Page.enable: it
// returns a script identifier and then never runs the script. So canvas noise,
// the WebGL strings, deviceMemory, languages, screen and the webdriver getter
// were all assembled correctly, sent successfully, and had no effect. Nothing
// in the reply says so — only reading the values back in a live page does.
func TestPatchScriptIsPrecededByPageEnable(t *testing.T) {
	t.Parallel()

	got := methods(Options{Fingerprint: profile.Fingerprint{CanvasNoise: true}})

	enable := slices.Index(got, "Page.enable")
	addScript := slices.Index(got, "Page.addScriptToEvaluateOnNewDocument")

	if addScript < 0 {
		t.Fatalf("no script injected for a canvas-noise profile: %v", got)
	}
	if enable < 0 {
		t.Fatalf("Page.enable missing — the injected script will not run: %v", got)
	}
	if enable > addScript {
		t.Errorf("Page.enable must come before the script is added, got %v", got)
	}
}

// TestBareProfileEnablesNoDomain is the counterweight to the Page.enable fix: a
// profile that requests no injected patches must inject nothing and enable
// nothing, leaving the CDP launch with no domain enabled at all — the property
// the cdp package was written for.
func TestBareProfileEnablesNoDomain(t *testing.T) {
	t.Parallel()

	got := methods(Options{
		Timezone:    testTZ,
		Fingerprint: profile.Fingerprint{UserAgent: "UA", Platform: testPlatform, HardwareConcurrent: 8},
	})

	for _, m := range got {
		if strings.HasSuffix(m, ".enable") {
			t.Errorf("bare profile enabled %s; it injects no script and needs no domain: %v", m, got)
		}
	}
}

// TestPageIsTheOnlyDomainEnabled keeps the concession minimal. Page.enable
// bought back the entire injected-patch layer, which does not run without it,
// and under --cdp-port an open debug port is a far louder signal than an
// enabled Page domain. That argument does not extend to any other domain —
// Runtime.enable especially is what anti-bot services probe for — and it does
// not license enabling Page more than once.
func TestPageIsTheOnlyDomainEnabled(t *testing.T) {
	t.Parallel()

	got := methods(Options{
		Timezone: testTZ,
		Fingerprint: profile.Fingerprint{
			UserAgent: "UA", Platform: testPlatform, CanvasNoise: true,
			HardwareConcurrent: 8, ScreenWidth: 1920, ScreenHeight: 1080,
		},
	})

	enables := 0
	for _, m := range got {
		if !strings.HasSuffix(m, ".enable") {
			continue
		}
		if m != "Page.enable" {
			t.Errorf("unexpected domain enabled: %s", m)
		}
		enables++
	}

	if enables != 1 {
		t.Errorf("Page.enable sent %d times, want exactly 1: %v", enables, got)
	}
}

// TestPlatformSurvivesWithoutAUserAgent covers a spoof that silently did not
// apply. navigator.platform and Accept-Language ride on
// Emulation.setUserAgentOverride, which the builder only emitted when the
// profile also pinned a user-agent — so `profile add x --platform Win32` set
// neither, while the confirmation page went on claiming the platform applied.
//
// The launcher now backfills the browser's own user-agent first, so this asserts
// the builder carries the platform once that has happened.
func TestPlatformSurvivesWithoutAUserAgent(t *testing.T) {
	t.Parallel()

	fp := profile.Fingerprint{Platform: testPlatform}

	if !needsUserAgentBackfill(fp) {
		t.Fatal("a pinned platform with no user-agent must be backfilled")
	}

	fp.UserAgent = "Mozilla/5.0 (backfilled by Browser.getVersion)"
	cmds := overrides(Options{Fingerprint: fp})

	for _, c := range cmds {
		if c.method != methodSetUA {
			continue
		}
		params, ok := c.params.(map[string]any)
		if !ok {
			t.Fatalf("%s params are %T, want a map", methodSetUA, c.params)
		}
		if params[platformKey] != testPlatform {
			t.Errorf("platform = %v, want Win32", params[platformKey])
		}

		return
	}

	t.Errorf("no %s command emitted: %v", methodSetUA, methods(Options{Fingerprint: fp}))
}

// TestUserAgentBackfillLeavesOrdinaryProfilesAlone keeps the backfill from
// firing when it would change nothing — an extra Browser.getVersion round trip
// on every launch, for a profile that pins neither platform nor language.
func TestUserAgentBackfillLeavesOrdinaryProfilesAlone(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		fp   profile.Fingerprint
		want bool
	}{
		{"nothing pinned", profile.Fingerprint{}, false},
		{"user-agent already set", profile.Fingerprint{UserAgent: "UA", Platform: testPlatform}, false},
		{"screen only", profile.Fingerprint{ScreenWidth: 1920, ScreenHeight: 1080}, false},
		{"platform only", profile.Fingerprint{Platform: testPlatform}, true},
		{"accept-language only", profile.Fingerprint{AcceptLanguage: "de-DE,de;q=0.9"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := needsUserAgentBackfill(tc.fp); got != tc.want {
				t.Errorf("needsUserAgentBackfill() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNavigateIsAlwaysLast pins the ordering the whole set depends on: the
// overrides and the injected script have to be in place before the page they
// are meant to dress is loaded.
func TestNavigateIsAlwaysLast(t *testing.T) {
	t.Parallel()

	got := methods(Options{
		Timezone: testTZ,
		Fingerprint: profile.Fingerprint{
			UserAgent:   "UA",
			Platform:    testPlatform,
			CanvasNoise: true,
			Geolocation: &profile.LatLng{Latitude: 52.52, Longitude: 13.40},
		},
	})

	if last := got[len(got)-1]; last != "Page.navigate" {
		t.Errorf("last command = %q, want Page.navigate; full order %v", last, got)
	}
}
