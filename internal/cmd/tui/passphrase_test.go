package tui //nolint:testpackage // white-box: drives the unexported passphrase modal and bridge

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// requestFor builds a passphrase request whose validate accepts only "open".
func requestFor(reply chan passphraseReply) passphraseRequestMsg {
	return passphraseRequestMsg{
		prompt:   "passphrase for /home/x/.ssh/id_ed25519:",
		validate: func(p []byte) error { return validateAgainst("open", p) },
		reply:    reply,
	}
}

// validateAgainst stands in for crypt.CheckSSHPassphrase in tests.
func validateAgainst(want string, got []byte) error {
	if string(got) != want {
		return errPassphraseCancelled // any non-nil error drives the retry path
	}

	return nil
}

func TestPassphraseModalShownOnRequest(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)
	m = asModel(t, mustUpdate(m, tea.WindowSizeMsg{Width: 100, Height: 30}))

	reply := make(chan passphraseReply, 1)
	next, _ := m.Update(requestFor(reply))
	m = asModel(t, next)

	if !m.passphrase.active {
		t.Fatal("expected passphrase modal to be active after a request")
	}
	content := m.View().Content
	for _, want := range []string{"unlock SSH key", "id_ed25519", "enter unlock"} {
		if !strings.Contains(content, want) {
			t.Errorf("modal view missing %q\n---\n%s", want, content)
		}
	}
}

func TestPassphraseWrongThenCorrect(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)
	reply := make(chan passphraseReply, 1)
	m = asModel(t, mustUpdate(m, requestFor(reply)))

	// A wrong passphrase must stay on the modal with an error, not resolve.
	m.passphrase.input.SetValue("nope")
	m = asModel(t, mustUpdate(m, key("enter")))
	if !m.passphrase.active {
		t.Fatal("expected modal to stay open after a wrong passphrase")
	}
	if m.passphrase.err == "" {
		t.Error("expected an error message after a wrong passphrase")
	}
	select {
	case <-reply:
		t.Fatal("must not reply to the waiting decrypt on a wrong passphrase")
	default:
	}

	// The correct passphrase resolves and closes the modal.
	m.passphrase.input.SetValue("open")
	m = asModel(t, mustUpdate(m, key("enter")))
	if m.passphrase.active {
		t.Fatal("expected modal to close after a correct passphrase")
	}
	got := <-reply
	if got.err != nil {
		t.Fatalf("reply error = %v, want nil", got.err)
	}
	if string(got.pass) != "open" {
		t.Errorf("reply pass = %q, want %q", got.pass, "open")
	}
}

func TestPassphraseCancelUnblocksDecrypt(t *testing.T) {
	t.Parallel()

	m, _ := newTestModel(t)
	reply := make(chan passphraseReply, 1)
	m = asModel(t, mustUpdate(m, requestFor(reply)))

	m = asModel(t, mustUpdate(m, tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})))
	if m.passphrase.active {
		t.Fatal("expected modal to close after esc")
	}
	got := <-reply
	if got.err == nil {
		t.Fatal("expected a cancellation error so the decrypt goroutine unblocks")
	}
}

// mustUpdate applies one message and returns the resulting model.
func mustUpdate(m Model, msg tea.Msg) tea.Model {
	next, _ := m.Update(msg)

	return next
}
