//go:build !windows

package crypt_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
)

var errDenied = errors.New("user denied access")

// fakeKeychain is an in-memory Keychain that counts its calls.
type fakeKeychain struct {
	items  map[string][]byte
	getErr error
	gets   int
	sets   int
}

func (k *fakeKeychain) Get(account string) ([]byte, error) {
	k.gets++
	if k.getErr != nil {
		return nil, k.getErr
	}
	p, ok := k.items[account]
	if !ok {
		return nil, crypt.ErrNotInKeychain
	}

	return p, nil
}

func (k *fakeKeychain) Set(account string, passphrase []byte) error {
	k.sets++
	k.items[account] = passphrase

	return nil
}

// countingPrompt stands in for the person at the terminal.
func countingPrompt(answer string, n *int) func(string) ([]byte, error) {
	return func(string) ([]byte, error) {
		*n++

		return []byte(answer), nil
	}
}

// TestKeychainPromptSavesThenReuses is the point of the feature: the first
// command asks and saves, a later command asks nobody.
func TestKeychainPromptSavesThenReuses(t *testing.T) {
	t.Parallel()

	key := encryptedKey(t)
	kc := &fakeKeychain{items: map[string][]byte{}}

	asked := 0
	first := crypt.KeychainPrompt(kc, key, countingPrompt(testPassphrase, &asked))
	for range 2 {
		if got, err := first("p: "); err != nil || string(got) != testPassphrase {
			t.Fatalf("first command = %q, %v", got, err)
		}
	}
	if asked != 1 || kc.sets != 1 {
		t.Fatalf("first command asked %d and saved %d times, want 1 and 1", asked, kc.sets)
	}

	second := crypt.KeychainPrompt(kc, key, countingPrompt("unused", &asked))
	if got, err := second("p: "); err != nil || string(got) != testPassphrase {
		t.Fatalf("second command = %q, %v", got, err)
	}
	if asked != 1 {
		t.Errorf("second command asked the person; want it served from the keychain")
	}
}

// TestKeychainPromptReplacesAStalePassphrase covers a key whose passphrase was
// changed after it was saved.
func TestKeychainPromptReplacesAStalePassphrase(t *testing.T) {
	t.Parallel()

	key := encryptedKey(t)
	kc := &fakeKeychain{items: map[string][]byte{}}
	if err := kc.Set(mustAbs(t, key), []byte("old passphrase")); err != nil {
		t.Fatal(err)
	}

	asked := 0
	got, err := crypt.KeychainPrompt(kc, key, countingPrompt(testPassphrase, &asked))("p: ")
	if err != nil || string(got) != testPassphrase {
		t.Fatalf("prompt = %q, %v", got, err)
	}
	if asked != 1 {
		t.Errorf("asked %d times, want 1", asked)
	}
	if string(kc.items[mustAbs(t, key)]) != testPassphrase {
		t.Errorf("keychain still holds the stale passphrase")
	}
}

// TestKeychainPromptFallsBackOnKeychainError keeps a locked or denied keychain
// from failing the command.
func TestKeychainPromptFallsBackOnKeychainError(t *testing.T) {
	t.Parallel()

	key := encryptedKey(t)
	kc := &fakeKeychain{items: map[string][]byte{}, getErr: errDenied}

	asked := 0
	got, err := crypt.KeychainPrompt(kc, key, countingPrompt(testPassphrase, &asked))("p: ")
	if err != nil || string(got) != testPassphrase {
		t.Fatalf("prompt = %q, %v", got, err)
	}
	if asked != 1 {
		t.Errorf("asked %d times, want 1", asked)
	}
}

// TestKeychainPromptDoesNotSaveAFailedPrompt keeps a cancelled or wrong entry
// out of the keychain.
func TestKeychainPromptDoesNotSaveAFailedPrompt(t *testing.T) {
	t.Parallel()

	key := encryptedKey(t)
	kc := &fakeKeychain{items: map[string][]byte{}}
	failing := func(string) ([]byte, error) { return nil, crypt.ErrWrongPassphrase }

	if _, err := crypt.KeychainPrompt(kc, key, failing)("p: "); !errors.Is(err, crypt.ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
	if kc.sets != 0 {
		t.Errorf("saved %d times after a failed prompt, want 0", kc.sets)
	}
}

func TestKeychainPromptNilKeychainIsPassThrough(t *testing.T) {
	t.Parallel()

	asked := 0
	p := crypt.KeychainPrompt(nil, "unused", countingPrompt("x", &asked))
	if _, err := p("p: "); err != nil || asked != 1 {
		t.Fatalf("nil keychain: err %v, asked %d", err, asked)
	}
}

func mustAbs(t *testing.T, p string) string {
	t.Helper()

	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatal(err)
	}

	return abs
}
