package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	domain "github.com/code-chorus-io/hamzad/internal/domain/profile"
)

// chromeVersion is the Chrome major version stamped into the spoofed user-agent
// strings below. Bump it as the real fleet's Chrome advances so the presets stay
// plausible against fingerprinting checks.
const chromeVersion = "140"

// selectOption is one choice in a dropdown: a human label plus the fingerprint
// mutation it applies when chosen.
type selectOption struct {
	label string
	apply func(*domain.Fingerprint)
}

// selectField is a single-line dropdown. Rather than pull in a popup widget it
// cycles a horizontal cursor over a fixed set of options with ←/→ (or h/l),
// following the cursor-based pattern already used by the dashboard table. It
// satisfies the form's field interface.
type selectField struct {
	name    string
	options []selectOption
	cursor  int
	focused bool
}

func (s *selectField) label() string { return s.name }
func (s *selectField) blur()         { s.focused = false }

func (s *selectField) focus() tea.Cmd {
	s.focused = true

	return nil
}

// selected returns the currently highlighted option.
func (s *selectField) selected() selectOption { return s.options[s.cursor] }

func (s *selectField) update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}

	switch key.String() {
	case "left", "h":
		s.cursor = (s.cursor - 1 + len(s.options)) % len(s.options)
	case "right", "l", " ":
		s.cursor = (s.cursor + 1) % len(s.options)
	}

	return nil
}

func (s *selectField) view() string {
	value := optionStyle
	if s.focused {
		value = focusStyle
	}

	return arrowStyle.Render("‹ ") + value.Render(s.selected().label) + arrowStyle.Render(" ›")
}

// newOSField builds the operating-system dropdown. Each non-default option sets
// navigator.platform together with a matching Chrome user-agent so the spoofed
// OS is coherent across both surfaces. The mobile presets (Android, iOS) only
// override platform/UA — the launcher does not yet emulate touch input, a mobile
// viewport, or the CDP mobile flag, so they read as mobile to UA/platform sniffs
// but not to a fingerprinter that also inspects screen size or maxTouchPoints.
func newOSField() *selectField {
	ua := func(system string) string {
		return "Mozilla/5.0 (" + system + ") AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
			chromeVersion + ".0.0.0 Safari/537.36"
	}

	return &selectField{
		name: "operating system",
		options: []selectOption{
			{label: "default (host)", apply: func(*domain.Fingerprint) {}},
			{label: "Windows 10/11", apply: func(fp *domain.Fingerprint) {
				fp.Platform = "Win32"
				fp.UserAgent = ua("Windows NT 10.0; Win64; x64")
			}},
			{label: "macOS", apply: func(fp *domain.Fingerprint) {
				fp.Platform = "MacIntel"
				fp.UserAgent = ua("Macintosh; Intel Mac OS X 10_15_7")
			}},
			{label: "Linux", apply: func(fp *domain.Fingerprint) {
				fp.Platform = "Linux x86_64"
				fp.UserAgent = ua("X11; Linux x86_64")
			}},
			{label: "Android (Pixel)", apply: func(fp *domain.Fingerprint) {
				fp.Platform = "Linux armv8l"
				fp.UserAgent = "Mozilla/5.0 (Linux; Android 14; Pixel 8) " +
					"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" +
					chromeVersion + ".0.0.0 Mobile Safari/537.36"
			}},
			{label: "iOS (iPhone)", apply: func(fp *domain.Fingerprint) {
				fp.Platform = "iPhone"
				fp.UserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) " +
					"AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1"
			}},
		},
	}
}

// newProcessorField builds the processor dropdown. Each preset is a coherent
// machine: it sets navigator.hardwareConcurrency (logical core count) together
// with the WebGL UNMASKED_VENDOR/RENDERER strings Chrome reports for that GPU,
// so the CPU and GPU signals agree. The renderer strings are real Chrome/ANGLE
// values — Metal on Apple silicon, Direct3D11 on Windows — so pick a preset that
// matches the operating-system dropdown for a fully consistent identity. The
// default leaves Chrome's own values untouched.
func newProcessorField() *selectField {
	machine := func(cores int, vendor, renderer string) func(*domain.Fingerprint) {
		return func(fp *domain.Fingerprint) {
			fp.HardwareConcurrent = cores
			fp.WebGLVendor = vendor
			fp.WebGLRenderer = renderer
		}
	}

	return &selectField{
		name: "processor",
		options: []selectOption{
			{label: "default (host)", apply: func(*domain.Fingerprint) {}},
			{label: "Apple M2 · 8-core", apply: machine(8, "Google Inc. (Apple)",
				"ANGLE (Apple, ANGLE Metal Renderer: Apple M2, Unspecified Version)")},
			{label: "Apple M1 · 8-core", apply: machine(8, "Google Inc. (Apple)",
				"ANGLE (Apple, ANGLE Metal Renderer: Apple M1, Unspecified Version)")},
			{label: "Intel i7 · UHD 630 · 8-core", apply: machine(8, "Google Inc. (Intel)",
				"ANGLE (Intel, Intel(R) UHD Graphics 630 (0x00003E9B) Direct3D11 vs_5_0 ps_5_0, D3D11)")},
			{label: "Intel i5 · Iris Xe · 8-core", apply: machine(8, "Google Inc. (Intel)",
				"ANGLE (Intel, Intel(R) Iris(R) Xe Graphics (0x0000A7A0) Direct3D11 vs_5_0 ps_5_0, D3D11)")},
			{label: "AMD Ryzen · Radeon · 12-core", apply: machine(12, "Google Inc. (AMD)",
				"ANGLE (AMD, AMD Radeon(TM) Graphics Direct3D11 vs_5_0 ps_5_0, D3D11)")},
			{label: "NVIDIA RTX 3060 · 12-core", apply: machine(12, "Google Inc. (NVIDIA)",
				"ANGLE (NVIDIA, NVIDIA GeForce RTX 3060 (0x00002503) Direct3D11 vs_5_0 ps_5_0, D3D11)")},
		},
	}
}

var (
	optionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	arrowStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)
