package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	domain "github.com/1995parham/koochooloologin/internal/domain/profile"
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
// OS is coherent across both surfaces.
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
		},
	}
}

// newProcessorField builds the processor dropdown. Options set
// navigator.hardwareConcurrency (the logical core count a site reads to profile
// the CPU); the default leaves Chrome's own value untouched.
func newProcessorField() *selectField {
	cores := func(n int) func(*domain.Fingerprint) {
		return func(fp *domain.Fingerprint) { fp.HardwareConcurrent = n }
	}

	return &selectField{
		name: "processor",
		options: []selectOption{
			{label: "default (host)", apply: func(*domain.Fingerprint) {}},
			{label: "2 cores", apply: cores(2)},
			{label: "4 cores", apply: cores(4)},
			{label: "6 cores", apply: cores(6)},
			{label: "8 cores", apply: cores(8)},
			{label: "12 cores", apply: cores(12)},
			{label: "16 cores", apply: cores(16)},
		},
	}
}

var (
	optionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	arrowStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)
