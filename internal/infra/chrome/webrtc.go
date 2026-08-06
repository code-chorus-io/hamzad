package chrome

// WebRTC IP handling policies Chrome accepts for --webrtc-ip-handling-policy.
// Verified against the shipped Chrome 151 binary.
const (
	// WebRTCDefault leaves Chrome alone: WebRTC may bind every interface and
	// send STUN over UDP, which goes around an HTTP proxy entirely.
	WebRTCDefault = "default"
	// WebRTCPublicOnly hides local addresses but still allows non-proxied UDP,
	// so the real public IP can still be observed.
	WebRTCPublicOnly = "default_public_interface_only"
	// WebRTCPublicAndPrivate is Chrome's widest setting; listed for completeness.
	WebRTCPublicAndPrivate = "default_public_and_private_interfaces"
	// WebRTCDisableNonProxiedUDP confines WebRTC to the proxy. Since the relay
	// is TCP-only, that means no UDP leaves at all — the setting that actually
	// closes the leak.
	WebRTCDisableNonProxiedUDP = "disable_non_proxied_udp"
)

// webRTCPolicies is the accepted set, for validation and for rejecting typos
// before Chrome silently ignores them.
var webRTCPolicies = map[string]struct{}{
	WebRTCDefault:              {},
	WebRTCPublicOnly:           {},
	WebRTCPublicAndPrivate:     {},
	WebRTCDisableNonProxiedUDP: {},
}

// ValidWebRTCMode reports whether mode is a policy Chrome understands. An empty
// mode is valid and means "decide from the profile".
func ValidWebRTCMode(mode string) bool {
	if mode == "" {
		return true
	}
	_, ok := webRTCPolicies[mode]

	return ok
}

// webRTCPolicy picks the effective IP handling policy for a launch.
//
// WebRTC is the most reliable way to unmask a proxied browser: it speaks STUN
// over UDP directly from the host's own interfaces, so it sidesteps an HTTP
// proxy completely and hands any page the real address — while every other
// signal says otherwise. That makes it worse than no proxy at all, because the
// mismatch also marks the profile as one that is trying to hide.
//
// So a profile with a proxy defaults to disable_non_proxied_udp: WebRTC is
// confined to the proxy, and since the relay carries only TCP, no UDP escapes.
// A profile without a proxy has nothing to leak and keeps Chrome's default, so
// video calls keep working. An explicit mode always wins.
func webRTCPolicy(o Options) string {
	if mode := o.Fingerprint.WebRTCMode; mode != "" {
		return mode
	}
	if o.Proxy != nil {
		return WebRTCDisableNonProxiedUDP
	}

	return ""
}

// webRTCLeaks reports whether a launch would let WebRTC reveal the host address
// despite a proxy being configured, so callers can warn about it.
func webRTCLeaks(o Options) bool {
	return o.Proxy != nil && webRTCPolicy(o) != WebRTCDisableNonProxiedUDP
}
