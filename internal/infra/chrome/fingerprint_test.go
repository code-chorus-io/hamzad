package chrome //nolint:testpackage // white-box: asserts the unexported patch builder and its mask

import (
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
)

// sampledPixels is how many leading pixels the mask assertions scan. Any real
// canvas fingerprint probe reads far more than this, so a pattern that behaves
// over this window behaves over a whole canvas.
const sampledPixels = 4096

// TestCanvasNoiseMaskPerturbsPixels is the regression guard for a mask that
// silently does nothing. The original pattern keyed off the byte index i, which
// steps by 4 and is therefore always even — so every `(i * k) & 1` evaluated to
// 0, every XOR was an identity, and a "noised" canvas hashed byte-identically to
// the host's while the feature reported itself as on. Nothing failed; the spoof
// was simply absent. So assert the mask actually flips bits.
func TestCanvasNoiseMaskPerturbsPixels(t *testing.T) {
	t.Parallel()

	for channel, shift := range canvasChannelShifts {
		flips := 0
		for p := range sampledPixels {
			mask := canvasNoiseMask(p, shift)
			if mask != 0 && mask != 1 {
				t.Fatalf("channel %d: mask at pixel %d = %d, want 0 or 1", channel, p, mask)
			}
			flips += mask
		}

		if flips == 0 {
			t.Errorf("channel %d (shift %d) never flips a bit — the noise is a no-op", channel, shift)
		}
		// A mask that fires on nearly every pixel or nearly none is a constant in
		// disguise; a healthy pattern lands near half.
		if flips < sampledPixels/4 || flips > 3*sampledPixels/4 {
			t.Errorf("channel %d (shift %d) flipped %d of %d pixels, want roughly half",
				channel, shift, flips, sampledPixels)
		}
	}
}

// TestCanvasNoiseChannelsAreIndependent checks the three channels do not flip in
// lockstep. Masks built from the pixel index alone (p*3, p*7, p*13) all inherit
// p's parity and would move together, which turns the per-channel noise into a
// single-channel one and makes the result easier to model.
func TestCanvasNoiseChannelsAreIndependent(t *testing.T) {
	t.Parallel()

	for a := range canvasChannelShifts {
		for b := a + 1; b < len(canvasChannelShifts); b++ {
			same := 0
			for p := range sampledPixels {
				if canvasNoiseMask(p, canvasChannelShifts[a]) == canvasNoiseMask(p, canvasChannelShifts[b]) {
					same++
				}
			}

			if same == sampledPixels {
				t.Errorf("channels %d and %d flip identically on every pixel", a, b)
			}
		}
	}
}

// TestCanvasNoiseScriptKeysOffPixelIndex pins the emitted JavaScript to the
// pixel index. The Go mask above can only vouch for the browser's behavior if
// the injected script computes the same expression, and the bug it replaces was
// exactly a script that indexed by the wrong variable.
func TestCanvasNoiseScriptKeysOffPixelIndex(t *testing.T) {
	t.Parallel()

	script := patchScript(profile.Fingerprint{CanvasNoise: true})

	if !strings.Contains(script, "const p = i >> 2") {
		t.Error("canvas noise script must derive a pixel index from the byte index")
	}
	for _, mask := range []string{"(p ^ (p >> 3)) & 1", "(p ^ (p >> 5)) & 1", "(p ^ (p >> 7)) & 1"} {
		if !strings.Contains(script, mask) {
			t.Errorf("canvas noise script missing mask %q", mask)
		}
	}
	// The byte index must never drive a mask again.
	for _, dead := range []string{"(i * 13)", "(i * 7)", "(i * 3)"} {
		if strings.Contains(script, dead) {
			t.Errorf("canvas noise script still masks off the byte index: %q is always even", dead)
		}
	}
}

// TestPatchScriptOmitsUnrequestedPatches keeps the injected surface minimal: a
// profile that asks for nothing still gets the webdriver fix (a plain Chrome
// reports false, so a CDP session must too) and nothing else.
func TestPatchScriptOmitsUnrequestedPatches(t *testing.T) {
	t.Parallel()

	script := patchScript(profile.Fingerprint{})

	if !strings.Contains(script, "webdriver") {
		t.Error("webdriver must always be neutralized")
	}
	for _, absent := range []string{"getImageData", "getParameter", hardwareConcurrencyKey, deviceMemoryKey} {
		if strings.Contains(script, absent) {
			t.Errorf("bare fingerprint should not patch %q", absent)
		}
	}
}

// TestHardwareConcurrencyIsNotAPageWorldPatch pins the Worker fix. A
// defineProperty on navigator only exists in the page's own JS world, so a
// fingerprinter that reads navigator.hardwareConcurrency inside a Worker got
// the host's real core count while everything else claimed otherwise — the
// mismatch being the tell, not the number. It is set through CDP's
// Emulation.setHardwareConcurrencyOverride instead, which the engine applies
// everywhere, so it must no longer appear in the injected script at all.
func TestHardwareConcurrencyIsNotAPageWorldPatch(t *testing.T) {
	t.Parallel()

	script := patchScript(profile.Fingerprint{HardwareConcurrent: 12, DeviceMemory: 16})

	if strings.Contains(script, hardwareConcurrencyKey) {
		t.Error("hardwareConcurrency must be an engine override, not an injected patch")
	}
	// deviceMemory has no CDP equivalent, so it is still a page-world patch.
	// Asserted so that the day one appears, this test says where to change.
	if !strings.Contains(script, deviceMemoryKey) {
		t.Error("deviceMemory is expected to remain an injected patch until CDP offers an override")
	}
}

// TestHardwareConcurrencyReachesTheCDPActions checks the override is actually
// issued, since removing the JS patch without adding it would be a regression
// dressed as a fix.
func TestHardwareConcurrencyReachesTheCDPActions(t *testing.T) {
	t.Parallel()

	var found bool
	for _, c := range overrides(Options{Fingerprint: profile.Fingerprint{HardwareConcurrent: 12}}) {
		if c.method == "Emulation.setHardwareConcurrencyOverride" {
			found = true
		}
	}

	if !found {
		t.Error("pinning hardwareConcurrency issued no engine override")
	}
}
