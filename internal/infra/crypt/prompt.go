package crypt

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"golang.org/x/term"
)

// ErrNoTerminal is returned when an encrypted SSH key needs its passphrase but
// there is neither a terminal to ask on nor an askpass program to ask through.
// Without it the failure surfaced as term's "operation not supported by
// device", which says nothing about what to do.
var ErrNoTerminal = errors.New(
	"passphrase needed but stdin is not a terminal; run hamzad from a terminal, " +
		"set askpass (HAMZAD_ASKPASS) to a program that prints the passphrase, or use an unencrypted age identity")

// passphraseAttempts is how many tries a person gets before the command fails,
// as with ssh and sudo.
const passphraseAttempts = 3

// TerminalPrompt returns a Crypt.Prompt for the SSH key at identityPath. It
// checks each answer against the key before accepting it, so a typo is caught
// at the prompt and retried instead of failing the decrypt that asked, and it
// remembers the accepted passphrase: one command decrypting a proxy secret and
// then a session bundle, or `store reencrypt` walking every artifact, asks
// once.
//
// When askpass is set the passphrase comes from running that program with the
// prompt as its argument, in the manner of SSH_ASKPASS; otherwise it is read
// from the controlling terminal.
func TerminalPrompt(identityPath, askpass string) func(prompt string) ([]byte, error) {
	var (
		mu     sync.Mutex
		cached []byte
	)

	return func(prompt string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()

		if cached != nil {
			return cached, nil
		}

		pass, err := askChecked(identityPath, askpass, prompt)
		if err != nil {
			return nil, err
		}
		cached = pass

		return pass, nil
	}
}

// askChecked asks for the passphrase until it unlocks the key or the attempts
// run out.
func askChecked(identityPath, askpass, prompt string) ([]byte, error) {
	read := readTerminal
	if askpass != "" {
		read = func(prompt string) ([]byte, error) { return runAskpass(askpass, prompt) }
	} else if !term.IsTerminal(int(os.Stdin.Fd())) {
		return nil, ErrNoTerminal
	}

	for attempt := 1; ; attempt++ {
		pass, err := read(prompt)
		if err != nil {
			return nil, err
		}

		err = CheckSSHPassphrase(identityPath, pass)
		if err == nil {
			return pass, nil
		}
		if !errors.Is(err, ErrWrongPassphrase) || attempt == passphraseAttempts {
			return nil, err
		}

		fmt.Fprintln(os.Stderr, "incorrect passphrase, try again")
	}
}

func readTerminal(prompt string) ([]byte, error) {
	fmt.Fprint(os.Stderr, prompt)
	pass, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("reading passphrase: %w", err)
	}

	return pass, nil
}

// runAskpass runs the configured program and takes its first line of output as
// the passphrase. Its stderr is passed through so a failing helper can say why.
func runAskpass(askpass, prompt string) ([]byte, error) {
	cmd := exec.Command(askpass, prompt) //nolint:gosec,noctx // G204: the operator configures askpass; it is interactive and must not be cut short
	cmd.Stderr = os.Stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("running askpass %q: %w", askpass, err)
	}

	line, _, _ := bytes.Cut(out, []byte("\n"))

	return bytes.TrimSuffix(line, []byte("\r")), nil
}
