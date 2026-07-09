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
	if fp.CanvasNoise {
		parts = append(parts, canvasNoisePatch())
	}

	if len(parts) == 0 {
		return ""
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

// canvasNoisePatch perturbs canvas readback so per-profile canvas hashes differ
// from the host's, without visibly altering rendered output.
func canvasNoisePatch() string {
	return `  try {
    const noisify = (canvas, ctx) => {
      try {
        const w = canvas.width, h = canvas.height;
        if (!w || !h) return;
        const img = ctx.getImageData(0, 0, w, h);
        for (let i = 0; i < img.data.length; i += 4) {
          img.data[i]   ^= (i * 13) & 1;
          img.data[i+1] ^= (i * 7)  & 1;
          img.data[i+2] ^= (i * 3)  & 1;
        }
        ctx.putImageData(img, 0, 0);
      } catch (e) {}
    };
    const toDataURL = HTMLCanvasElement.prototype.toDataURL;
    HTMLCanvasElement.prototype.toDataURL = function () {
      const ctx = this.getContext('2d');
      if (ctx) noisify(this, ctx);
      return toDataURL.apply(this, arguments);
    };
    const getImageData = CanvasRenderingContext2D.prototype.getImageData;
    CanvasRenderingContext2D.prototype.getImageData = function () {
      const res = getImageData.apply(this, arguments);
      for (let i = 0; i < res.data.length; i += 4) res.data[i] ^= (i * 5) & 1;
      return res;
    };
  } catch (e) {}`
}
