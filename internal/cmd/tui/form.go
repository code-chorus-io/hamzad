package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	domain "github.com/1995parham/koochooloologin/internal/domain/profile"
)

// field is one focusable row in the add-profile form: either a free-text input
// or a fixed-choice dropdown. Rendering reads through the interface while focus
// and edit changes mutate the concrete value, so implementations use pointer
// receivers and the form holds them as pointers.
type field interface {
	label() string
	focus() tea.Cmd
	blur()
	update(msg tea.Msg) tea.Cmd
	view() string
}

// textField is a single free-text row wrapping a Bubbles textinput.
type textField struct {
	name  string
	input textinput.Model
}

func newTextField(name, placeholder string) *textField {
	in := textinput.New()
	in.Placeholder = placeholder
	in.Prompt = "> "
	in.CharLimit = 256

	return &textField{name: name, input: in}
}

func (t *textField) label() string  { return t.name }
func (t *textField) focus() tea.Cmd { return t.input.Focus() }
func (t *textField) blur()          { t.input.Blur() }
func (t *textField) view() string   { return t.input.View() }

// value returns the trimmed text the user typed.
func (t *textField) value() string { return strings.TrimSpace(t.input.Value()) }

func (t *textField) update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	t.input, cmd = t.input.Update(msg)

	return cmd
}

// formModel is the interactive add-profile screen. Its rendering methods read
// the model by value while the focus helpers mutate it through pointer
// receivers, so the receiver kinds are intentionally mixed.
//
//nolint:recvcheck // mixed value/pointer receivers are intentional (see above).
type formModel struct {
	fields []field
	focus  int
	err    string
}

// newFormModel builds the ordered set of rows. The first (name) is required; the
// operating-system and processor dropdowns spoof the matching fingerprint
// fields, and the remaining text rows mirror the most-used flags of
// `profile add`. Advanced fingerprint fields (WebGL, screen, device memory, …)
// stay on the CLI for now.
func newFormModel() formModel {
	return formModel{
		fields: []field{
			newTextField("name", "required, e.g. acme-us"),
			newOSField(),
			newProcessorField(),
			newTextField("proxy", "socks5://user:pass@host:1080 (optional)"),
			newTextField("timezone", "Europe/Berlin (optional)"),
			newTextField("start-url", "https://… (optional)"),
			newTextField("notes", "free-form note (optional)"),
		},
	}
}

// focusCmd focuses the first row when the form opens.
func (f *formModel) focusCmd() tea.Cmd {
	return f.fields[f.focus].focus()
}

// updateForm handles the add-profile screen.
func (m Model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, m.form.updateFocused(msg)
	}

	switch key.String() {
	case "esc":
		m.view = viewDashboard
		m.status = "cancelled"

		return m, nil

	case keyCtrlC:
		return m, tea.Quit

	case "tab", "down":
		return m, m.form.focusNext()

	case "shift+tab", "up":
		return m, m.form.focusPrev()

	case "enter":
		// Enter advances between rows and submits from the last one.
		if m.form.focus < len(m.form.fields)-1 {
			return m, m.form.focusNext()
		}

		return m.submitForm()

	case "ctrl+s":
		return m.submitForm()
	}

	// Everything else (including ←/→ for the dropdowns) goes to the focused row.
	return m, m.form.updateFocused(msg)
}

// submitForm validates the inputs, persists the new profile (and its encrypted
// proxy, when given), and returns to the dashboard.
func (m Model) submitForm() (tea.Model, tea.Cmd) {
	name := m.form.text("name")
	if name == "" {
		m.form.err = "name is required"

		return m, nil
	}

	proxy := m.form.text("proxy")
	if proxy != "" && !m.crypt.Configured() {
		m.form.err = "encryption not configured; run 'store init' before adding a proxy"

		return m, nil
	}

	p := domain.Profile{
		Name:     name,
		Proxy:    proxy,
		Timezone: m.form.text("timezone"),
		StartURL: m.form.text("start-url"),
		Notes:    m.form.text("notes"),
	}
	m.form.applyFingerprint(&p.Fingerprint)

	if err := m.store.Add(p); err != nil {
		m.form.err = err.Error()

		return m, nil
	}

	if proxy != "" {
		if err := m.store.SetProxy(m.crypt, name, proxy); err != nil {
			m.form.err = "profile saved but proxy failed: " + err.Error()

			return m, nil
		}
	}

	m.view = viewDashboard
	m.status = "created profile " + name
	m.profiles = append(m.profiles, p)

	return m, m.reloadCmd()
}

// text returns the trimmed content of the text field with the given label.
func (f formModel) text(label string) string {
	for _, fl := range f.fields {
		if t, ok := fl.(*textField); ok && t.name == label {
			return t.value()
		}
	}

	return ""
}

// applyFingerprint layers the current dropdown selections onto fp. Each dropdown
// has a no-op default, so untouched rows leave Chrome's own values in place.
func (f formModel) applyFingerprint(fp *domain.Fingerprint) {
	for _, fl := range f.fields {
		if s, ok := fl.(*selectField); ok {
			s.selected().apply(fp)
		}
	}
}

// focusNext moves focus to the next row, wrapping around.
func (f *formModel) focusNext() tea.Cmd {
	return f.setFocus((f.focus + 1) % len(f.fields))
}

// focusPrev moves focus to the previous row, wrapping around.
func (f *formModel) focusPrev() tea.Cmd {
	return f.setFocus((f.focus - 1 + len(f.fields)) % len(f.fields))
}

func (f *formModel) setFocus(i int) tea.Cmd {
	f.fields[f.focus].blur()
	f.focus = i

	return f.fields[f.focus].focus()
}

// updateFocused forwards a message to the focused row.
func (f *formModel) updateFocused(msg tea.Msg) tea.Cmd {
	return f.fields[f.focus].update(msg)
}

func (f formModel) view() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("new profile") + "\n\n")

	for i, fl := range f.fields {
		label := fl.label()
		if i == f.focus {
			label = focusStyle.Render("▸ " + label)
		} else {
			label = "  " + label
		}
		b.WriteString(labelStyle.Render(padRight(label, 20)))
		b.WriteString(fl.view())
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if f.err != "" {
		b.WriteString(statusStyle.Render(f.err) + "\n\n")
	}
	b.WriteString(helpStyle.Render(
		"tab/↑↓ move  •  ←/→ change option  •  enter next/submit  •  ctrl+s save  •  esc cancel"))

	return b.String()
}

// padRight pads s with spaces to at least width visible columns.
func padRight(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}

	return s
}

var (
	labelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	focusStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
)
