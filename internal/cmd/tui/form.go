package tui

import (
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	domain "github.com/1995parham/koochooloologin/internal/domain/profile"
)

// formField describes one text input in the add-profile form.
type formField struct {
	label       string
	placeholder string
}

// formFields is the ordered set of inputs. The first (name) is required; the
// rest mirror the most-used flags of `profile add`. Advanced fingerprint fields
// (WebGL, screen, device memory, …) stay on the CLI for now.
var formFields = []formField{
	{label: "name", placeholder: "required, e.g. acme-us"},
	{label: "proxy", placeholder: "socks5://user:pass@host:1080 (optional)"},
	{label: "timezone", placeholder: "Europe/Berlin (optional)"},
	{label: "start-url", placeholder: "https://… (optional)"},
	{label: "notes", placeholder: "free-form note (optional)"},
}

// formModel is the interactive add-profile screen. Its rendering methods read
// the model by value while the focus helpers mutate it through pointer
// receivers, so the receiver kinds are intentionally mixed.
//
//nolint:recvcheck // mixed value/pointer receivers are intentional (see above).
type formModel struct {
	inputs []textinput.Model
	focus  int
	err    string
}

func newFormModel() formModel {
	inputs := make([]textinput.Model, len(formFields))
	for i, f := range formFields {
		in := textinput.New()
		in.Placeholder = f.placeholder
		in.Prompt = "> "
		in.CharLimit = 256
		inputs[i] = in
	}

	return formModel{inputs: inputs}
}

// focusCmd focuses the first input when the form opens.
func (f *formModel) focusCmd() tea.Cmd {
	return f.inputs[f.focus].Focus()
}

// updateForm handles the add-profile screen.
func (m Model) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, m.form.updateInputs(msg)
	}

	switch key.String() {
	case "esc":
		m.view = viewDashboard
		m.status = "cancelled"

		return m, nil

	case "ctrl+c":
		return m, tea.Quit

	case "tab", "down":
		return m, m.form.focusNext()

	case "shift+tab", "up":
		return m, m.form.focusPrev()

	case "enter":
		// Enter advances between fields and submits from the last one.
		if m.form.focus < len(m.form.inputs)-1 {
			return m, m.form.focusNext()
		}

		return m.submitForm()

	case "ctrl+s":
		return m.submitForm()
	}

	return m, m.form.updateInputs(msg)
}

// submitForm validates the inputs, persists the new profile (and its encrypted
// proxy, when given), and returns to the dashboard.
func (m Model) submitForm() (tea.Model, tea.Cmd) {
	name := strings.TrimSpace(m.form.value("name"))
	if name == "" {
		m.form.err = "name is required"

		return m, nil
	}

	proxy := strings.TrimSpace(m.form.value("proxy"))
	if proxy != "" && !m.crypt.Configured() {
		m.form.err = "encryption not configured; run 'store init' before adding a proxy"

		return m, nil
	}

	p := domain.Profile{
		Name:     name,
		Proxy:    proxy,
		Timezone: strings.TrimSpace(m.form.value("timezone")),
		StartURL: strings.TrimSpace(m.form.value("start-url")),
		Notes:    strings.TrimSpace(m.form.value("notes")),
	}

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

// value returns the trimmed content of the field with the given label.
func (f formModel) value(label string) string {
	for i, ff := range formFields {
		if ff.label == label {
			return f.inputs[i].Value()
		}
	}

	return ""
}

// focusNext moves focus to the next input, wrapping around.
func (f *formModel) focusNext() tea.Cmd {
	return f.setFocus((f.focus + 1) % len(f.inputs))
}

// focusPrev moves focus to the previous input, wrapping around.
func (f *formModel) focusPrev() tea.Cmd {
	return f.setFocus((f.focus - 1 + len(f.inputs)) % len(f.inputs))
}

func (f *formModel) setFocus(i int) tea.Cmd {
	f.inputs[f.focus].Blur()
	f.focus = i

	return f.inputs[f.focus].Focus()
}

// updateInputs forwards a message to the focused input.
func (f *formModel) updateInputs(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)

	return cmd
}

func (f formModel) view() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("new profile") + "\n\n")

	for i, field := range formFields {
		label := field.label
		if i == f.focus {
			label = focusStyle.Render("▸ " + label)
		} else {
			label = "  " + label
		}
		b.WriteString(labelStyle.Render(padRight(label, 14)))
		b.WriteString(f.inputs[i].View())
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if f.err != "" {
		b.WriteString(statusStyle.Render(f.err) + "\n\n")
	}
	b.WriteString(helpStyle.Render("tab/↑↓ move  •  enter next/submit  •  ctrl+s save  •  esc cancel"))

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
