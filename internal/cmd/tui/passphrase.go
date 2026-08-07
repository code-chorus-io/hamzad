package tui

import (
	"errors"
	"sync"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
)

// errPassphraseCancelled unblocks a waiting decrypt when the user dismisses the
// prompt, turning the abort into an ordinary "cancelled" session error rather
// than a hang.
var errPassphraseCancelled = errors.New("passphrase entry cancelled")

// passphraseReply carries the outcome of a modal back to the manager goroutine
// blocked inside crypt's passphrase callback.
type passphraseReply struct {
	pass []byte
	err  error
}

// passphraseRequestMsg asks the UI to unlock the SSH identity. It is sent from a
// manager goroutine (via passphraseBridge) onto the Bubble Tea loop, where it is
// the only thing allowed to touch the model. validate is called on the typed
// bytes so a wrong passphrase is rejected in-place, and reply delivers the
// accepted passphrase (or a cancellation) back to the waiting goroutine.
type passphraseRequestMsg struct {
	prompt   string
	validate func([]byte) error
	reply    chan<- passphraseReply
}

// passphraseBridge adapts crypt's synchronous passphrase callback — invoked deep
// inside a decrypt on a manager goroutine — to the asynchronous Bubble Tea loop.
// It serializes concurrent requests and caches the first accepted passphrase, so
// opening several profiles at once unlocks the key exactly once.
type passphraseBridge struct {
	identityPath string

	gate sync.Mutex // one modal at a time; later openers wait, then hit the cache

	mu     sync.Mutex
	prog   *tea.Program
	cached []byte
}

func newPassphraseBridge(identityPath string) *passphraseBridge {
	return &passphraseBridge{identityPath: identityPath}
}

// attach wires the running program in once it exists (the program cannot be
// built until the model, which needs the bridge's prompt, already is).
func (b *passphraseBridge) attach(prog *tea.Program) {
	b.mu.Lock()
	b.prog = prog
	b.mu.Unlock()
}

// prompt satisfies crypt.Crypt.Prompt. It blocks the calling goroutine until the
// user submits or cancels, and returns the cached passphrase without a second
// modal once the key has been unlocked.
func (b *passphraseBridge) prompt(prompt string) ([]byte, error) {
	b.gate.Lock()
	defer b.gate.Unlock()

	b.mu.Lock()
	cached, prog := b.cached, b.prog
	b.mu.Unlock()

	if cached != nil {
		return cached, nil
	}
	if prog == nil {
		return nil, errPassphraseCancelled
	}

	reply := make(chan passphraseReply, 1)
	prog.Send(passphraseRequestMsg{
		prompt:   prompt,
		validate: func(pass []byte) error { return crypt.CheckSSHPassphrase(b.identityPath, pass) },
		reply:    reply,
	})

	r := <-reply
	if r.err != nil {
		return nil, r.err
	}

	b.mu.Lock()
	b.cached = r.pass
	b.mu.Unlock()

	return r.pass, nil
}

// passphraseModel is the modal that collects the SSH key passphrase. It masks
// input, validates on submit, and reports a rejected passphrase inline so the
// user can retry without the surrounding session launch failing.
//
//nolint:recvcheck // value receiver for View, pointer receivers for mutation.
type passphraseModel struct {
	active   bool
	input    textinput.Model
	prompt   string
	err      string
	validate func([]byte) error
	reply    chan<- passphraseReply
}

// begin arms the modal for a fresh request and returns the command that starts
// the cursor blinking.
func (p *passphraseModel) begin(msg passphraseRequestMsg) tea.Cmd {
	in := textinput.New()
	in.Prompt = "> "
	in.Placeholder = "passphrase"
	in.EchoMode = textinput.EchoPassword
	in.EchoCharacter = '•'
	in.CharLimit = 256

	p.active = true
	p.input = in
	p.prompt = msg.prompt
	p.err = ""
	p.validate = msg.validate
	p.reply = msg.reply

	return p.input.Focus()
}

// resolve delivers a reply to the waiting goroutine and closes the modal. A nil
// pass with a non-nil err is a cancellation.
func (p *passphraseModel) resolve(pass []byte, err error) {
	if p.reply != nil {
		p.reply <- passphraseReply{pass: pass, err: err}
	}
	p.active = false
	p.reply = nil
	p.validate = nil
	p.input.SetValue("")
}

// update handles key input while the modal is up. Enter validates and, on
// success, hands the passphrase back; a wrong passphrase stays on screen with an
// error. Esc cancels the pending decrypt.
func (p *passphraseModel) update(msg tea.Msg) tea.Cmd {
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)

		return cmd
	}

	switch key.String() {
	case "esc", keyCtrlC:
		p.resolve(nil, errPassphraseCancelled)

		return nil

	case keyEnter:
		pass := []byte(p.input.Value())
		if p.validate != nil {
			if err := p.validate(pass); err != nil {
				p.err = validationMessage(err)
				p.input.SetValue("")

				return nil
			}
		}
		p.resolve(pass, nil)

		return nil
	}

	p.err = ""
	var cmd tea.Cmd
	p.input, cmd = p.input.Update(msg)

	return cmd
}

// validationMessage renders a passphrase-check error for the modal, keeping the
// common wrong-passphrase case short.
func validationMessage(err error) string {
	if errors.Is(err, crypt.ErrWrongPassphrase) {
		return "incorrect passphrase — try again"
	}

	return err.Error()
}

func (p passphraseModel) view(width, height int) string {
	title := passTitleStyle.Render("🔒 unlock SSH key")
	body := title + "\n\n" +
		labelStyle.Render(p.prompt) + "\n\n" +
		p.input.View() + "\n"
	if p.err != "" {
		body += "\n" + statusStyle.Render(p.err) + "\n"
	}
	body += "\n" + helpStyle.Render("enter unlock  •  esc cancel")

	box := passBoxStyle.Render(body)
	if width <= 0 || height <= 0 {
		return box
	}

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

var (
	passTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("63"))
	passBoxStyle   = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63")).
			Padding(1, 3)
)
