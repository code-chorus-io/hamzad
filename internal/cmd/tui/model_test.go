package tui //nolint:testpackage // white-box: drives unexported Model/Update/submitForm

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	domain "github.com/1995parham/koochooloologin/internal/domain/profile"
	"github.com/1995parham/koochooloologin/internal/infra/crypt"
	"github.com/1995parham/koochooloologin/internal/infra/manager"
	"github.com/1995parham/koochooloologin/internal/infra/store"
)

// newTestModel builds a model over a temp store seeded with two profiles.
func newTestModel(t *testing.T) (Model, *store.Store) {
	t.Helper()

	st := store.New(t.TempDir())
	for _, name := range []string{"alpha", "beta"} {
		if err := st.Add(domain.Profile{Name: name}); err != nil {
			t.Fatalf("seeding %q: %v", name, err)
		}
	}

	c := crypt.New(st.RecipientsPath(), "")
	mgr := manager.New(st, c, "")

	m, err := newModel(context.Background(), st, c, mgr)
	if err != nil {
		t.Fatalf("newModel: %v", err)
	}

	return m, st
}

// asModel converts an updated tea.Model back to the concrete Model.
func asModel(t *testing.T, tm tea.Model) Model {
	t.Helper()

	m, ok := tm.(Model)
	if !ok {
		t.Fatalf("expected tui.Model, got %T", tm)
	}

	return m
}

// key builds a printable key-press message.
func key(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: rune(s[0]), Text: s})
}

func TestDashboardRendersProfiles(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)

	next, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = asModel(t, next)

	content := m.View().Content
	for _, want := range []string{"koochooloologin — sessions", "NAME", "alpha", "beta"} {
		if !strings.Contains(content, want) {
			t.Errorf("dashboard view missing %q\n---\n%s", want, content)
		}
	}
}

func TestSwitchToFormAndSubmitPersistsProfile(t *testing.T) {
	t.Parallel()

	m, st := newTestModel(t)

	// "a" opens the add-profile form.
	next, _ := m.Update(key("a"))
	m = asModel(t, next)
	if m.view != viewForm {
		t.Fatalf("expected form view, got %v", m.view)
	}

	form := m.View().Content
	for _, want := range []string{"new profile", "name", "proxy"} {
		if !strings.Contains(form, want) {
			t.Errorf("form view missing %q\n---\n%s", want, form)
		}
	}

	// Type a name and submit. The name is the first row (a text field).
	nameField, ok := m.form.fields[0].(*textField)
	if !ok {
		t.Fatalf("expected first field to be a text field, got %T", m.form.fields[0])
	}
	nameField.input.SetValue("gamma")
	next, _ = m.submitForm()
	m = asModel(t, next)

	if m.view != viewDashboard {
		t.Fatalf("expected to return to dashboard after submit, got %v", m.view)
	}
	if _, err := st.Get("gamma"); err != nil {
		t.Fatalf("profile %q was not persisted: %v", "gamma", err)
	}
}

func TestSubmitFormRequiresName(t *testing.T) {
	t.Parallel()

	m, st := newTestModel(t)

	next, _ := m.Update(key("a"))
	m = asModel(t, next)

	// Submit with an empty name: it must stay on the form with an error.
	next, _ = m.submitForm()
	m = asModel(t, next)

	if m.view != viewForm {
		t.Fatalf("expected to stay on form for empty name, got %v", m.view)
	}
	if m.form.err == "" {
		t.Error("expected a validation error for empty name")
	}
	if profiles, _ := st.List(); len(profiles) != 2 {
		t.Errorf("expected no new profile, store has %d", len(profiles))
	}
}

// findSelect returns the dropdown row with the given label, failing if absent.
func findSelect(t *testing.T, f formModel, label string) *selectField {
	t.Helper()

	for _, fl := range f.fields {
		if s, ok := fl.(*selectField); ok && s.name == label {
			return s
		}
	}
	t.Fatalf("form has no %q dropdown", label)

	return nil
}

func TestFormDropdownsSpoofFingerprint(t *testing.T) {
	t.Parallel()

	m, st := newTestModel(t)

	next, _ := m.Update(key("a"))
	m = asModel(t, next)

	name, ok := m.form.fields[0].(*textField)
	if !ok {
		t.Fatalf("expected first field to be a text field, got %T", m.form.fields[0])
	}
	name.input.SetValue("winbox")

	// Advance the OS dropdown one step (default → Windows) and the processor
	// dropdown four steps (default → 2 → 4 → 6 → 8 cores). "l" cycles forward.
	findSelect(t, m.form, "operating system").update(key("l"))
	cpu := findSelect(t, m.form, "processor")
	for range 4 {
		cpu.update(key("l"))
	}

	next, _ = m.submitForm()
	m = asModel(t, next)

	if m.view != viewDashboard {
		t.Fatalf("expected to return to dashboard after submit, got %v", m.view)
	}

	p, err := st.Get("winbox")
	if err != nil {
		t.Fatalf("profile %q was not persisted: %v", "winbox", err)
	}
	if p.Fingerprint.Platform != "Win32" {
		t.Errorf("Platform = %q, want Win32", p.Fingerprint.Platform)
	}
	if !strings.Contains(p.Fingerprint.UserAgent, "Windows NT 10.0") {
		t.Errorf("UserAgent = %q, want a Windows UA", p.Fingerprint.UserAgent)
	}
	if p.Fingerprint.HardwareConcurrent != 8 {
		t.Errorf("HardwareConcurrent = %d, want 8", p.Fingerprint.HardwareConcurrent)
	}
}
