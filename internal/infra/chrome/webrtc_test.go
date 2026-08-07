package chrome //nolint:testpackage // white-box: exercises the unexported WebRTC policy choice

import (
	"slices"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
)

// TestWebRTCDefaultsClosedWhenProxied is the important one. WebRTC speaks STUN
// over UDP straight from the host's interfaces, so it goes around an HTTP proxy
// and hands any page the real address while every other signal claims otherwise
// — a mismatch that is worse than not proxying at all. A proxied profile must
// therefore be leak-safe without anyone remembering to ask.
func TestWebRTCDefaultsClosedWhenProxied(t *testing.T) {
	t.Parallel()

	o := Options{ProxySpec: "socks5://user:pass@1.2.3.4:1080"}

	if got := webRTCPolicy(o); got != WebRTCDisableNonProxiedUDP {
		t.Errorf("proxied profile policy = %q, want %q", got, WebRTCDisableNonProxiedUDP)
	}
	if webRTCLeaks(o) {
		t.Error("a proxied profile must not be reported as leaking by default")
	}
}

// TestWebRTCUntouchedWithoutProxy keeps the fix from breaking ordinary use: a
// profile with no proxy has no address to hide, and disabling non-proxied UDP
// would silently break video calls.
func TestWebRTCUntouchedWithoutProxy(t *testing.T) {
	t.Parallel()

	if got := webRTCPolicy(Options{}); got != "" {
		t.Errorf("unproxied profile policy = %q, want Chrome's own default", got)
	}
	if webRTCLeaks(Options{}) {
		t.Error("a profile with no proxy has nothing to leak")
	}
}

// TestWebRTCExplicitModeWins lets someone who needs UDP through deliberately
// override the safe default — and be reported as leaking, since they are.
func TestWebRTCExplicitModeWins(t *testing.T) {
	t.Parallel()

	o := Options{
		ProxySpec:   "socks5://1.2.3.4:1080",
		Fingerprint: profile.Fingerprint{WebRTCMode: WebRTCPublicOnly},
	}

	if got := webRTCPolicy(o); got != WebRTCPublicOnly {
		t.Errorf("policy = %q, want the explicit mode", got)
	}
	if !webRTCLeaks(o) {
		t.Error("a proxied profile allowing non-proxied UDP is leaking and should say so")
	}
}

// TestWebRTCFlagReachesTheCleanLaunch pins the policy to the clean path. Unlike
// the CDP overrides, this one is a plain command-line flag — so it is the rare
// protection that survives the default, no-CDP launch, which is exactly where a
// leak would otherwise go unnoticed.
func TestWebRTCFlagReachesTheCleanLaunch(t *testing.T) {
	t.Parallel()

	o := Options{UserDataDir: "/tmp/p", ProxySpec: "socks5://1.2.3.4:1080"}

	args := cleanArgs(o, "http://127.0.0.1:9")
	want := "--webrtc-ip-handling-policy=" + WebRTCDisableNonProxiedUDP
	if !slices.Contains(args, want) {
		t.Errorf("clean launch missing %q; got %v", want, args)
	}
}

// TestValidWebRTCMode rejects typos, which Chrome would otherwise ignore in
// silence — leaving the profile leaking while the config claims otherwise.
func TestValidWebRTCMode(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{"", WebRTCDefault, WebRTCPublicOnly, WebRTCPublicAndPrivate, WebRTCDisableNonProxiedUDP} {
		if !ValidWebRTCMode(ok) {
			t.Errorf("ValidWebRTCMode(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"disable", "off", "disable-non-proxied-udp", "DISABLE_NON_PROXIED_UDP"} {
		if ValidWebRTCMode(bad) {
			t.Errorf("ValidWebRTCMode(%q) = true, want false", bad)
		}
	}
}
