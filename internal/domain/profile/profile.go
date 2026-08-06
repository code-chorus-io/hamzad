// Package profile defines the browser-profile domain model and its validation.
// It is pure: no filesystem, network, or browser dependencies live here.
package profile

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
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

// ErrUnsupportedProxyScheme is returned for a proxy URL whose scheme Chrome
// cannot use.
var ErrUnsupportedProxyScheme = errors.New("unsupported proxy scheme (want http, https, socks5, socks4)")

// ErrProxyMissingHost is returned when a proxy URL has no host:port.
var ErrProxyMissingHost = errors.New("proxy is missing host:port")

// ErrProxyAuthUnsupported is returned for a proxy that carries credentials on a
// scheme with no username/password auth method. SOCKS4 authenticates only a
// bare userid (no password), so a user:pass on it cannot be honored. HTTP,
// HTTPS, and SOCKS5 credentials are supported: koochooloologin routes an
// authenticated proxy through a local relay (see infra/chrome) that performs the
// upstream handshake, since Chrome cannot supply proxy credentials itself.
var ErrProxyAuthUnsupported = errors.New("proxy scheme cannot authenticate credentials (SOCKS4 has no user/pass auth; use http, https, or socks5 for user:pass)")

// nameRE constrains profile names to a filesystem- and git-friendly shape so a
// name can double as a directory component in the store.
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// Profile is a self-contained browser identity: an isolated Chrome user-data
// directory plus the proxy, timezone, and fingerprint applied when it launches.
type Profile struct {
	Name  string `json:"name"            koanf:"name"  toml:"name"`
	Notes string `json:"notes,omitempty" koanf:"notes" toml:"notes,omitempty"`
	// Proxy is never written to the plaintext profiles.toml (toml:"-"); it is
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

	if p.Proxy != "" {
		if _, err := ParseProxy(p.Proxy); err != nil {
			return err
		}
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

// supportedProxySchemes are the proxy transports Chrome understands via
// --proxy-server. SOCKS4 is included for completeness though rarely used.
var supportedProxySchemes = map[string]struct{}{
	"http":   {},
	"https":  {},
	"socks5": {},
	"socks4": {},
}

// ParseProxy parses a proxy URL of the form scheme://[user:pass@]host:port and
// validates its scheme. Credentials, when present, are preserved on the result
// for the launcher to feed to Chrome's Fetch auth handler.
func ParseProxy(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing proxy %q: %w", raw, err)
	}

	if _, ok := supportedProxySchemes[u.Scheme]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedProxyScheme, u.Scheme)
	}

	if u.Host == "" {
		return nil, fmt.Errorf("%w: %q", ErrProxyMissingHost, raw)
	}

	// SOCKS4 has no username/password auth method (only a bare userid), so
	// credentials on it cannot be honored; reject them up front rather than let
	// them be dropped and the connection fail opaquely. HTTP/HTTPS/SOCKS5
	// credentials are handled by the launch-time auth relay.
	if u.User != nil && u.Scheme == "socks4" {
		return nil, fmt.Errorf("%w: %q", ErrProxyAuthUnsupported, u.Scheme)
	}

	return u, nil
}
