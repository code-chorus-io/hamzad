package tui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	domain "github.com/1995parham/koochooloologin/internal/domain/profile"
	"github.com/1995parham/koochooloologin/internal/infra/crypt"
	"github.com/1995parham/koochooloologin/internal/infra/manager"
	"github.com/1995parham/koochooloologin/internal/infra/store"
)

// view selects which screen the model is showing.
type view int

const (
	viewDashboard view = iota
	viewForm
)

// sessionEventMsg carries a manager session state change onto the UI loop.
type sessionEventMsg struct{ session manager.Session }

// tickMsg drives the once-a-second refresh of session uptimes.
type tickMsg time.Time

// openResultMsg reports the outcome of an Open attempt (errors only; success is
// observed through session events).
type openResultMsg struct {
	name string
	err  error
}

// profilesMsg carries a freshly reloaded profile list.
type profilesMsg struct {
	profiles []domain.Profile
	err      error
}

// Model is the root Bubble Tea model. It shows the session dashboard and, on
// demand, the interactive add-profile form. Bubble Tea's interface methods take
// value receivers while internal mutators (refreshRows, rowFor) use pointer
// receivers, so the receiver kinds are intentionally mixed.
//
//nolint:recvcheck // mixed value/pointer receivers are intentional (see above).
type Model struct {
	ctx   context.Context //nolint:containedctx // carried for geo lookups during Open
	store *store.Store
	crypt crypt.Crypt
	mgr   *manager.Manager

	view     view
	table    table.Model
	profiles []domain.Profile
	form     formModel

	width  int
	height int
	status string
}

func newModel(ctx context.Context, st *store.Store, c crypt.Crypt, mgr *manager.Manager) (Model, error) {
	profiles, err := st.List()
	if err != nil {
		return Model{}, err
	}

	t := table.New(
		table.WithColumns(dashboardColumns()),
		table.WithFocused(true),
		table.WithHeight(12),
	)

	m := Model{
		ctx:      ctx,
		store:    st,
		crypt:    c,
		mgr:      mgr,
		view:     viewDashboard,
		table:    t,
		profiles: profiles,
	}
	m.refreshRows()

	return m, nil
}

func dashboardColumns() []table.Column {
	return []table.Column{
		{Title: "NAME", Width: 18},
		{Title: "SESSION", Width: 9},
		{Title: "UPTIME", Width: 9},
		{Title: "PROXY", Width: 6},
		{Title: "PORT", Width: 6},
		{Title: "NOTES", Width: 24},
	}
}

// Init starts the periodic uptime refresh.
func (m Model) Init() tea.Cmd {
	return tick()
}

// Update handles messages for whichever view is active.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.resize(msg), nil
	case tickMsg:
		return m.handleTick()
	case sessionEventMsg:
		m.status = sessionStatusLine(msg.session)
		m.refreshRows()

		return m, nil
	case openResultMsg:
		return m.handleOpenResult(msg), nil
	case profilesMsg:
		return m.handleProfiles(msg), nil
	}

	if m.view == viewForm {
		return m.updateForm(msg)
	}

	return m.updateDashboard(msg)
}

// resize fits the table to the new terminal dimensions.
func (m Model) resize(msg tea.WindowSizeMsg) Model {
	m.width, m.height = msg.Width, msg.Height
	m.table.SetWidth(msg.Width)
	if h := msg.Height - 8; h > 3 {
		m.table.SetHeight(h)
	}

	return m
}

// handleTick refreshes uptimes and reschedules the next tick.
func (m Model) handleTick() (tea.Model, tea.Cmd) {
	if m.view == viewDashboard {
		m.refreshRows()
	}

	return m, tick()
}

// handleOpenResult surfaces an Open failure on the status line.
func (m Model) handleOpenResult(msg openResultMsg) Model {
	if msg.err != nil {
		m.status = fmt.Sprintf("open %q: %v", msg.name, msg.err)
	}

	return m
}

// handleProfiles applies a reloaded profile list.
func (m Model) handleProfiles(msg profilesMsg) Model {
	if msg.err != nil {
		m.status = "reload: " + msg.err.Error()

		return m
	}
	m.profiles = msg.profiles
	m.refreshRows()

	return m
}

// updateDashboard handles key input for the session table.
func (m Model) updateDashboard(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		m.table, cmd = m.table.Update(msg)

		return m, cmd
	}

	switch key.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "a", "n":
		m.form = newFormModel()
		m.view = viewForm
		m.status = ""

		return m, m.form.focusCmd()

	case "o", "enter":
		if name := m.selectedName(); name != "" {
			m.status = "opening " + name + "…"

			return m, m.openCmd(name)
		}

		return m, nil

	case "c", "x":
		if name := m.selectedName(); name != "" {
			m.mgr.Close(name)
			m.status = "closing " + name + "…"
		}

		return m, nil

	case "r":
		return m, m.reloadCmd()
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)

	return m, cmd
}

// View renders the active screen in the alternate screen buffer.
func (m Model) View() tea.View {
	var body string
	if m.view == viewForm {
		body = m.form.view()
	} else {
		body = m.dashboardView()
	}

	return tea.View{
		Content:     body,
		AltScreen:   true,
		WindowTitle: "koochooloologin",
	}
}

func (m Model) dashboardView() string {
	help := helpStyle.Render(
		"↑/↓ move  •  o/enter open  •  c close  •  a add profile  •  r reload  •  q quit")

	view := titleStyle.Render("koochooloologin — sessions") + "\n\n" +
		m.table.View() + "\n\n" + help
	if m.status != "" {
		view += "\n" + statusStyle.Render(m.status)
	}

	return view
}

// selectedName returns the profile name under the table cursor, or "".
func (m Model) selectedName() string {
	row := m.table.SelectedRow()
	if len(row) == 0 {
		return ""
	}

	return row[0]
}

// refreshRows rebuilds the table from the current profiles and live session
// state, preserving the cursor position.
func (m *Model) refreshRows() {
	rows := make([]table.Row, 0, len(m.profiles))
	for _, p := range m.profiles {
		rows = append(rows, m.rowFor(p))
	}

	cursor := m.table.Cursor()
	m.table.SetRows(rows)
	if cursor < len(rows) {
		m.table.SetCursor(cursor)
	}
}

func (m *Model) rowFor(p domain.Profile) table.Row {
	sessionCol, uptimeCol, portCol := "-", "-", "-"
	if s, ok := m.mgr.Get(p.Name); ok {
		sessionCol = s.Status.String()
		if s.Status.Active() {
			uptimeCol = uptime(s.StartedAt)
			if s.DebugPort > 0 {
				portCol = strconv.Itoa(s.DebugPort)
			}
		}
	}

	proxyCol := "-"
	if m.store.HasSecret(p.Name) {
		proxyCol = "enc"
	}

	return table.Row{p.Name, sessionCol, uptimeCol, proxyCol, portCol, orDash(p.Notes)}
}

// openCmd opens a profile off the UI goroutine so bundle restore and option
// build do not block rendering.
func (m Model) openCmd(name string) tea.Cmd {
	return func() tea.Msg {
		err := m.mgr.Open(m.ctx, name, manager.OpenOpts{})

		return openResultMsg{name: name, err: err}
	}
}

// reloadCmd re-reads the profile list from the store.
func (m Model) reloadCmd() tea.Cmd {
	return func() tea.Msg {
		profiles, err := m.store.List()

		return profilesMsg{profiles: profiles, err: err}
	}
}

// tick schedules the next uptime refresh.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func sessionStatusLine(s manager.Session) string {
	if s.Status == manager.StatusError && s.Err != nil {
		return fmt.Sprintf("%s: %v", s.Name, s.Err)
	}

	return fmt.Sprintf("%s: %s", s.Name, s.Status)
}

// uptime renders how long a session has been running, at second resolution.
func uptime(since time.Time) string {
	d := max(time.Since(since).Truncate(time.Second), 0)

	return d.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}

	return s
}

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	helpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
)
