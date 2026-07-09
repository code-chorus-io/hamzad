package chrome

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/1995parham/koochooloologin/internal/domain/profile"
)

// patchScript builds the JavaScript injected via
// Page.addScriptToEvaluateOnNewDocument to spoof the fingerprint signals that
// have no CDP override. This is best-effort (Tier 2): the patches live in the
// page's JS world and are detectable by sophisticated probes (worker re-reads,
// Function.toString checks). CDP-native overrides in chrome.go are the reliable
// layer; this covers the gaps a plain Chromium leaves open. Returns "" when the
// fingerprint requests none of these patches.
func patchScript(fp profile.Fingerprint) string {
	// Always neutralize the automation tell first: chromedp's CDP attach sets
	// navigator.webdriver to true, which trips bot checks (e.g. Google's
	// sign-in block). A real, non-automated Chrome reports false.
	parts := []string{defineNavigator("webdriver", false)}

	if fp.HardwareConcurrent > 0 {
		parts = append(parts, defineNavigator("hardwareConcurrency", fp.HardwareConcurrent))
	}
	if fp.DeviceMemory > 0 {
		parts = append(parts, defineNavigator("deviceMemory", fp.DeviceMemory))
	}
	if len(fp.Languages) > 0 {
		parts = append(parts, defineNavigator("languages", fp.Languages))
	}
	if fp.WebGLVendor != "" || fp.WebGLRenderer != "" {
		parts = append(parts, webglPatch(fp.WebGLVendor, fp.WebGLRenderer))
	}
	if fp.ScreenWidth > 0 && fp.ScreenHeight > 0 {
		parts = append(parts, screenPatch(fp.ScreenWidth, fp.ScreenHeight))
	}
	if fp.CanvasNoise {
		parts = append(parts, canvasNoisePatch())
	}

	return "(() => {\n" + strings.Join(parts, "\n") + "\n})();"
}

// defineNavigator emits a defineProperty override for a navigator property.
func defineNavigator(prop string, value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}

	return fmt.Sprintf(
		`  try { Object.defineProperty(Object.getPrototypeOf(navigator), %q, { get: () => %s, configurable: true }); } catch (e) {}`,
		prop, string(raw),
	)
}

// webglPatch overrides the UNMASKED_VENDOR/RENDERER strings returned by
// WebGLRenderingContext.getParameter for both WebGL 1 and 2 contexts.
func webglPatch(vendor, renderer string) string {
	v, err := json.Marshal(vendor)
	if err != nil {
		return ""
	}
	r, err := json.Marshal(renderer)
	if err != nil {
		return ""
	}

	return fmt.Sprintf(`  try {
    const V = %s, R = %s;
    const patch = (proto) => {
      const orig = proto.getParameter;
      proto.getParameter = function (p) {
        if (V && p === 37445) return V;      // UNMASKED_VENDOR_WEBGL
        if (R && p === 37446) return R;      // UNMASKED_RENDERER_WEBGL
        return orig.apply(this, arguments);
      };
    };
    if (window.WebGLRenderingContext) patch(WebGLRenderingContext.prototype);
    if (window.WebGL2RenderingContext) patch(WebGL2RenderingContext.prototype);
  } catch (e) {}`, string(v), string(r))
}

// screenPatch overrides the window.screen dimensions that CDP's
// Emulation.setDeviceMetricsOverride leaves reporting the host monitor on a
// desktop page, so screen.width/height match the profile's declared size. availWidth/availHeight mirror the full size since no host chrome is being emulated.
func screenPatch(w, h int) string {
	return fmt.Sprintf(`  try {
    const dims = { width: %d, height: %d, availWidth: %d, availHeight: %d };
    for (const [k, v] of Object.entries(dims)) {
      try { Object.defineProperty(Object.getPrototypeOf(screen), k, { get: () => v, configurable: true }); } catch (e) {}
    }
  } catch (e) {}`, w, h, w, h)
}

// canvasNoisePatch perturbs canvas readback so per-profile canvas hashes differ
// from the host's, without visibly altering rendered output.
//
// The perturbation flips the low bit of each RGB channel by a fixed,
// position-derived pattern, so it is deterministic: the same canvas always
// hashes to the same (host-distinct) value, and the toDataURL and getImageData
// paths agree with each other. Crucially, the noise is only ever applied to a
// copy of the pixels being read out — the source canvas is never written back
// to — so repeated reads stay stable and the visible canvas is never corrupted.
func canvasNoisePatch() string {
	return `  try {
    // Flip the low bit of the R/G/B channels by a fixed per-position pattern.
    const noise = (data) => {
      for (let i = 0; i < data.length; i += 4) {
        data[i]   ^= (i * 13) & 1;
        data[i+1] ^= (i * 7)  & 1;
        data[i+2] ^= (i * 3)  & 1;
      }
    };
    const rawGetImageData = CanvasRenderingContext2D.prototype.getImageData;
    CanvasRenderingContext2D.prototype.getImageData = function () {
      // getImageData already returns a fresh copy, so noising it in place does
      // not touch the canvas itself.
      const res = rawGetImageData.apply(this, arguments);
      noise(res.data);
      return res;
    };
    const rawToDataURL = HTMLCanvasElement.prototype.toDataURL;
    HTMLCanvasElement.prototype.toDataURL = function () {
      const w = this.width, h = this.height;
      const ctx = w && h ? this.getContext('2d') : null;
      if (!ctx) return rawToDataURL.apply(this, arguments);
      try {
        // Serialize a detached copy with the noise baked in; the original
        // canvas is left untouched, so successive calls return the same value.
        const copy = document.createElement('canvas');
        copy.width = w; copy.height = h;
        const cctx = copy.getContext('2d');
        cctx.drawImage(this, 0, 0);
        const img = rawGetImageData.call(cctx, 0, 0, w, h);
        noise(img.data);
        cctx.putImageData(img, 0, 0);
        return rawToDataURL.apply(copy, arguments);
      } catch (e) {
        return rawToDataURL.apply(this, arguments);
      }
    };
  } catch (e) {}`
}
