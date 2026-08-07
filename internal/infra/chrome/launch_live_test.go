//go:build live

// Package-local integration test, behind the `live` build tag because it needs
// a real browser and a few seconds of wall clock:
//
//	go test -tags live ./internal/infra/chrome/
//
// It exists because every other test in this package asserts that the right CDP
// commands are *built*, and the launcher's two worst bugs were commands that
// were built perfectly, sent successfully, and had no effect: the session was
// closed straight after sending (Chrome reverts every Emulation.* override when
// it detaches) and Page.enable was missing (addScriptToEvaluateOnNewDocument
// returns a script id and then never runs). Neither is visible in a protocol
// reply. Only reading the values back out of a live page catches them.
package chrome

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/cdp"
)

// readback is what the page reports once the launcher has dressed it.
type readback struct {
	UserAgent           string   `json:"userAgent"`
	Platform            string   `json:"platform"`
	Languages           []string `json:"languages"`
	Screen              string   `json:"screen"`
	DeviceMemory        int      `json:"deviceMemory"`
	HardwareConcurrency int      `json:"hardwareConcurrency"`
	WebGLVendor         string   `json:"webglVendor"`
	Timezone            string   `json:"timezone"`
	Webdriver           bool     `json:"webdriver"`
}

const readbackExpr = `JSON.stringify({
  userAgent: navigator.userAgent,
  platform: navigator.platform,
  languages: navigator.languages,
  screen: screen.width + "x" + screen.height,
  deviceMemory: navigator.deviceMemory || 0,
  hardwareConcurrency: navigator.hardwareConcurrency,
  webglVendor: (() => { try {
    const gl = document.createElement("canvas").getContext("webgl");
    const ext = gl.getExtension("WEBGL_debug_renderer_info");
    return String(gl.getParameter(ext.UNMASKED_VENDOR_WEBGL));
  } catch (e) { return "" } })(),
  timezone: Intl.DateTimeFormat().resolvedOptions().timeZone,
  webdriver: navigator.webdriver,
})`

// TestLaunchCDPAppliesEveryOverride launches a fully-specified profile and reads
// every spoofed signal back out of the page it lands on.
func TestLaunchCDPAppliesEveryOverride(t *testing.T) {
	if Detect() == "" {
		t.Skip("no chrome/chromium on this machine")
	}

	const (
		wantUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
			"(KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36"
		wantVendor = "Google Inc. (NVIDIA)"
		wantTZ     = "Asia/Tokyo"
	)

	port := freePort(t)
	opts := Options{
		UserDataDir: filepath.Join(t.TempDir(), "profile"),
		Timezone:    wantTZ,
		DebugPort:   port,
		Fingerprint: profile.Fingerprint{
			UserAgent:      wantUA,
			Platform:       "Win32",
			AcceptLanguage: "de-DE,de;q=0.9",
			Languages:      []string{"de-DE", "de"},
			ScreenWidth:    1920,
			ScreenHeight:   1080,
			DeviceMemory:   4,
			WebGLVendor:    wantVendor,
			CanvasNoise:    true,
			// Deliberately not the host's core count, so a passing assertion
			// cannot be the host value in disguise.
			HardwareConcurrent: 3,
		},
	}

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Launch(ctx, opts) }()
	defer func() {
		cancel()
		<-done
	}()

	got := readPage(ctx, t, port)

	if got.UserAgent != wantUA {
		t.Errorf("userAgent = %q, want %q", got.UserAgent, wantUA)
	}
	if got.Platform != "Win32" {
		t.Errorf("navigator.platform = %q, want Win32", got.Platform)
	}
	if len(got.Languages) != 2 || got.Languages[0] != "de-DE" || got.Languages[1] != "de" {
		t.Errorf("navigator.languages = %v, want [de-DE de]", got.Languages)
	}
	if got.Screen != "1920x1080" {
		t.Errorf("screen = %q, want 1920x1080", got.Screen)
	}
	if got.DeviceMemory != 4 {
		t.Errorf("deviceMemory = %d, want 4", got.DeviceMemory)
	}
	if got.HardwareConcurrency != 3 {
		t.Errorf("hardwareConcurrency = %d, want 3", got.HardwareConcurrency)
	}
	if got.WebGLVendor != wantVendor {
		t.Errorf("WebGL vendor = %q, want %q", got.WebGLVendor, wantVendor)
	}
	if got.Timezone != wantTZ {
		t.Errorf("timezone = %q, want %q", got.Timezone, wantTZ)
	}
	if got.Webdriver {
		t.Error("navigator.webdriver = true, want false")
	}
}

// TestLaunchCDPPlatformWithoutAUserAgent covers the profile that pins a platform
// and nothing else: setUserAgentOverride is the only carrier for it, and it
// needs a user-agent, so the launcher backfills the browser's own.
func TestLaunchCDPPlatformWithoutAUserAgent(t *testing.T) {
	if Detect() == "" {
		t.Skip("no chrome/chromium on this machine")
	}

	port := freePort(t)
	opts := Options{
		UserDataDir: filepath.Join(t.TempDir(), "profile"),
		DebugPort:   port,
		Fingerprint: profile.Fingerprint{Platform: "Win32"},
	}

	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Launch(ctx, opts) }()
	defer func() {
		cancel()
		<-done
	}()

	got := readPage(ctx, t, port)

	if got.Platform != "Win32" {
		t.Errorf("navigator.platform = %q, want Win32", got.Platform)
	}
	if got.UserAgent == "" {
		t.Error("user-agent is empty; the backfill should have supplied the browser's own")
	}
}

// readPage attaches a second CDP client — the way an external automation tool
// would — and evaluates the readback expression in the launched page.
func readPage(ctx context.Context, t *testing.T, port int) readback {
	t.Helper()

	wsURL, err := waitForEndpoint(ctx, port)
	if err != nil {
		t.Fatalf("waiting for the devtools endpoint: %v", err)
	}

	// The launcher navigates after it finishes dressing the page; give that
	// navigation a moment to land before reading.
	time.Sleep(2 * time.Second)

	client, err := cdp.Dial(ctx, wsURL)
	if err != nil {
		t.Fatalf("dialling devtools: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.AttachToPage(ctx); err != nil {
		t.Fatalf("attaching to the page: %v", err)
	}

	raw, err := client.Send(ctx, "Runtime.evaluate", map[string]any{
		"expression": readbackExpr, "returnByValue": true,
	})
	if err != nil {
		t.Fatalf("evaluating the readback: %v", err)
	}

	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decoding the evaluate reply: %v", err)
	}

	var got readback
	if err := json.Unmarshal([]byte(res.Result.Value), &got); err != nil {
		t.Fatalf("decoding the readback %q: %v", res.Result.Value, err)
	}

	return got
}

// freePort reserves a port the OS is not using, so parallel runs and a busy
// machine do not collide on a hardcoded 9222.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}

	return port
}
