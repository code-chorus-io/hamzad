// Package profile defines the browser-profile domain model and its validation.
// It is pure: no filesystem, network, or browser dependencies live here.
package profile

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ErrInvalidWebRTCMode is returned for a WebRTC policy Chrome does not accept.
// Chrome ignores an unknown value in silence, which would leave the profile
// leaking while its config claims otherwise — so it is rejected up front.
var ErrInvalidWebRTCMode = errors.New(
	"invalid webrtc mode (want disable_non_proxied_udp, default_public_interface_only, " +
		"default_public_and_private_interfaces or default)")

// webRTCModes are the policies Chrome accepts for --webrtc-ip-handling-policy.
var webRTCModes = map[string]struct{}{
	"default":                               {},
	"default_public_interface_only":         {},
	"default_public_and_private_interfaces": {},
	"disable_non_proxied_udp":               {},
}

// ErrInvalidName is returned when a profile name is empty or malformed.
var ErrInvalidName = errors.New("invalid profile name")

// ErrInvalidProxy is returned for a proxy spec that is neither a URL with a
// scheme and host nor a raw sing-box outbound JSON object.
var ErrInvalidProxy = errors.New("invalid proxy (want scheme://host:port or a sing-box outbound JSON object)")

// nameRE constrains profile names to a filesystem- and git-friendly shape so a
// name can double as a directory component in the store.
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// Profile is a self-contained browser identity: an isolated Chrome user-data
// directory plus the proxy, timezone, and fingerprint applied when it launches.
type Profile struct {
	Name  string `json:"name"            koanf:"name"  toml:"name"`
	Notes string `json:"notes,omitempty" koanf:"notes" toml:"notes,omitempty"`
	// Proxy is the upstream as the user wrote it: a share link (socks5://,
	// vless://, trojan://, ss://) or a raw sing-box outbound JSON object. It is
	// never written to the plaintext profiles.toml (toml:"-"); it is
	// persisted encrypted in the store's secrets/ directory. It is populated in
	// memory only when a command explicitly resolves it.
	Proxy       string      `json:"proxy,omitempty"     koanf:"proxy"       toml:"-"`
	Timezone    string      `json:"timezone,omitempty"  koanf:"timezone"    toml:"timezone,omitempty"`
	StartURL    string      `json:"start_url,omitempty" koanf:"start_url"   toml:"start_url,omitempty"`
	Fingerprint Fingerprint `json:"fingerprint"         koanf:"fingerprint" toml:"fingerprint"`
}

// Fingerprint captures the per-profile spoofable browser characteristics. Zero
// values mean "leave Chrome's default" so a minimal profile stays realistic.
type Fingerprint struct {
	UserAgent          string   `json:"user_agent,omitempty"           koanf:"user_agent"           toml:"user_agent,omitempty"`
	Platform           string   `json:"platform,omitempty"             koanf:"platform"             toml:"platform,omitempty"`
	AcceptLanguage     string   `json:"accept_language,omitempty"      koanf:"accept_language"      toml:"accept_language,omitempty"`
	Languages          []string `json:"languages,omitempty"            koanf:"languages"            toml:"languages,omitempty"`
	ScreenWidth        int      `json:"screen_width,omitempty"         koanf:"screen_width"         toml:"screen_width,omitempty"`
	ScreenHeight       int      `json:"screen_height,omitempty"        koanf:"screen_height"        toml:"screen_height,omitempty"`
	HardwareConcurrent int      `json:"hardware_concurrency,omitempty" koanf:"hardware_concurrency" toml:"hardware_concurrency,omitempty"`
	DeviceMemory       int      `json:"device_memory,omitempty"        koanf:"device_memory"        toml:"device_memory,omitempty"`
	WebGLVendor        string   `json:"webgl_vendor,omitempty"         koanf:"webgl_vendor"         toml:"webgl_vendor,omitempty"`
	WebGLRenderer      string   `json:"webgl_renderer,omitempty"       koanf:"webgl_renderer"       toml:"webgl_renderer,omitempty"`
	CanvasNoise        bool     `json:"canvas_noise,omitempty"         koanf:"canvas_noise"         toml:"canvas_noise,omitempty"`
	WebRTCMode         string   `json:"webrtc_mode,omitempty"          koanf:"webrtc_mode"          toml:"webrtc_mode,omitempty"`
	Geolocation        *LatLng  `json:"geolocation,omitempty"          koanf:"geolocation"          toml:"geolocation,omitempty"`
}

// LatLng is a geographic coordinate override, typically derived from the proxy
// IP so that navigator.geolocation matches the apparent network location.
type LatLng struct {
	Latitude  float64 `json:"latitude"           koanf:"latitude"  toml:"latitude"`
	Longitude float64 `json:"longitude"          koanf:"longitude" toml:"longitude"`
	Accuracy  float64 `json:"accuracy,omitempty" koanf:"accuracy"  toml:"accuracy,omitempty"`
}

// Validate reports whether the profile is well-formed. It checks the name
// shape, that any proxy URL parses with a supported scheme, and that any
// timezone names a loadable IANA zone.
func (p Profile) Validate() error {
	if !nameRE.MatchString(p.Name) {
		return fmt.Errorf("%w: %q (use letters, digits, '-' or '_', max 64 chars)", ErrInvalidName, p.Name)
	}

	if err := ValidateProxySpec(p.Proxy); err != nil {
		return err
	}

	if m := p.Fingerprint.WebRTCMode; m != "" {
		if _, ok := webRTCModes[m]; !ok {
			return fmt.Errorf("%w: %q", ErrInvalidWebRTCMode, m)
		}
	}

	if p.Timezone != "" {
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			return fmt.Errorf("invalid timezone %q: %w", p.Timezone, err)
		}
	}

	return nil
}

// ValidateProxySpec checks a proxy spec is syntactically plausible.
//
// This is deliberately shallow. Which protocols are actually supported is a
// property of the proxy layer, not the domain — and that layer speaks a dozen
// of them, several with no URL form at all. So the domain only asks whether the
// value is one of the two shapes a spec can take, and the launcher rejects a
// scheme it cannot map.
func ValidateProxySpec(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	// A raw sing-box outbound object; its contents are the proxy layer's business.
	if strings.HasPrefix(raw, "{") {
		return nil
	}

	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidProxy, err)
	}
	if u.Scheme == "" {
		return fmt.Errorf("%w: %q has no scheme", ErrInvalidProxy, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: %q has no host:port", ErrInvalidProxy, raw)
	}

	return nil
}
