//go:build !windows

package crypt_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
)

const testPassphrase = "correct horse"

// encryptedKey writes a passphrase-protected ed25519 SSH key and returns its path.
func encryptedKey(t *testing.T) string {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(testPassphrase))
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}

	path := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("writing key: %v", err)
	}

	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	if err := os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(pub), 0o600); err != nil {
		t.Fatalf("writing public key: %v", err)
	}

	return path
}

// askpass writes a helper that prints answers in turn, one per run, and logs
// every run so a test can count how often the passphrase was asked for.
func askpass(t *testing.T, answers ...string) (string, string) {
	t.Helper()

	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	var script strings.Builder
	script.WriteString("#!/bin/sh\necho \"$1\" >> \"" + calls + "\"\nn=$(wc -l < \"" + calls + "\")\ncase $n in\n")
	for i, a := range answers {
		fmt.Fprintf(&script, "  %d) echo '%s' ;;\n", i+1, a)
	}
	script.WriteString("  *) exit 1 ;;\nesac\n")

	path := filepath.Join(dir, "askpass")
	if err := os.WriteFile(path, []byte(script.String()), 0o700); err != nil { //nolint:gosec // test helper must be executable
		t.Fatalf("writing askpass: %v", err)
	}

	return path, calls
}

func callCount(t *testing.T, calls string) int {
	t.Helper()

	b, err := os.ReadFile(calls) //nolint:gosec // test-owned temp file
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("reading call log: %v", err)
	}

	return strings.Count(string(b), "\n")
}

// TestPromptAsksOncePerCommand is the reason the prompt exists: opening a
// profile decrypts the proxy and then the session bundle, and that must not
// mean typing the passphrase twice.
func TestPromptAsksOncePerCommand(t *testing.T) {
	t.Parallel()

	key := encryptedKey(t)
	helper, calls := askpass(t, testPassphrase)
	prompt := crypt.TerminalPrompt(key, helper)

	for range 3 {
		got, err := prompt("passphrase: ")
		if err != nil {
			t.Fatalf("prompt: %v", err)
		}
		if string(got) != testPassphrase {
			t.Fatalf("passphrase = %q, want %q", got, testPassphrase)
		}
	}

	if n := callCount(t, calls); n != 1 {
		t.Errorf("asked %d times, want once", n)
	}
}

// TestPromptRetriesAWrongPassphrase catches the typo at the prompt instead of
// failing the decrypt that asked for it.
func TestPromptRetriesAWrongPassphrase(t *testing.T) {
	t.Parallel()

	key := encryptedKey(t)
	helper, calls := askpass(t, "typo", testPassphrase)

	got, err := crypt.TerminalPrompt(key, helper)("passphrase: ")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if string(got) != testPassphrase {
		t.Errorf("passphrase = %q, want %q", got, testPassphrase)
	}
	if n := callCount(t, calls); n != 2 {
		t.Errorf("asked %d times, want 2", n)
	}
}

// TestPromptGivesUpAfterThreeTries keeps a wrong helper from looping forever.
func TestPromptGivesUpAfterThreeTries(t *testing.T) {
	t.Parallel()

	key := encryptedKey(t)
	helper, calls := askpass(t, "one", "two", "three", testPassphrase)

	_, err := crypt.TerminalPrompt(key, helper)("passphrase: ")
	if !errors.Is(err, crypt.ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
	if n := callCount(t, calls); n != 3 {
		t.Errorf("asked %d times, want 3", n)
	}
}

// TestPromptWithoutTerminalSaysWhatToDo replaces term's "operation not
// supported by device" with an error that names the way out.
func TestPromptWithoutTerminalSaysWhatToDo(t *testing.T) {
	t.Parallel()

	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal here; the no-terminal path cannot be reached")
	}

	_, err := crypt.TerminalPrompt(encryptedKey(t), "")("passphrase: ")
	if !errors.Is(err, crypt.ErrNoTerminal) {
		t.Fatalf("err = %v, want ErrNoTerminal", err)
	}
}

// TestDecryptsShareOneUnlock runs the real decrypt path: two artifacts, as
// `profile open` decrypts a proxy secret and then a session bundle, and one ask.
func TestDecryptsShareOneUnlock(t *testing.T) {
	t.Parallel()

	key := encryptedKey(t)
	pub, err := os.ReadFile(key + ".pub") //nolint:gosec // test-owned temp file
	if err != nil {
		t.Fatalf("reading public key: %v", err)
	}
	recipients := filepath.Join(t.TempDir(), "recipients.txt")
	if err := os.WriteFile(recipients, pub, 0o600); err != nil { //nolint:gosec // G703: test-owned temp path
		t.Fatalf("writing recipients: %v", err)
	}

	helper, calls := askpass(t, testPassphrase)
	c := crypt.New(recipients, key)
	c.Prompt = crypt.TerminalPrompt(key, helper)

	for _, secret := range []string{"socks5://u:p@h:1080", "bundle bytes"} {
		ct, err := c.EncryptBytes([]byte(secret))
		if err != nil {
			t.Fatalf("encrypt: %v", err)
		}
		pt, err := c.DecryptBytes(ct)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		if string(pt) != secret {
			t.Fatalf("round trip = %q, want %q", pt, secret)
		}
	}

	if n := callCount(t, calls); n != 1 {
		t.Errorf("asked %d times across two decrypts, want once", n)
	}
}
