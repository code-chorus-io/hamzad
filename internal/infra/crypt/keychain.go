package crypt

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ErrNotInKeychain is returned by Keychain.Get when no passphrase is stored for
// the identity.
var ErrNotInKeychain = errors.New("passphrase not in keychain")

// ErrBadKeychainAccount is returned when an identity path cannot be written
// into a keychain command.
var ErrBadKeychainAccount = errors.New("identity path cannot be used as a keychain account")

// ErrKeychainUnsupported is returned by SystemKeychain on a platform that has
// no keychain hamzad knows how to use.
var ErrKeychainUnsupported = errors.New("keychain is only supported on macOS")

// Keychain stores SSH key passphrases, keyed by the identity's path.
type Keychain interface {
	Get(account string) ([]byte, error)
	Set(account string, passphrase []byte) error
}

// KeychainPrompt wraps next, the prompt that asks a person, so the passphrase
// comes from kc when it holds one that unlocks the key at identityPath. On a
// miss, or when the stored passphrase no longer unlocks the key, it asks next
// and saves the answer, so the next command does not ask at all. Keychain
// failures are reported on stderr and never fail the command: the prompt is
// always there to fall back on.
//
// A nil kc returns next unchanged.
func KeychainPrompt(kc Keychain, identityPath string, next func(string) ([]byte, error)) func(string) ([]byte, error) {
	if kc == nil {
		return next
	}

	account := identityPath
	if abs, err := filepath.Abs(identityPath); err == nil {
		account = abs
	}

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

		stored, err := kc.Get(account)
		switch {
		case err == nil:
			if CheckSSHPassphrase(identityPath, stored) == nil {
				cached = stored

				return stored, nil
			}
			fmt.Fprintln(os.Stderr, "note: the passphrase in the keychain no longer unlocks the key; asking instead")
		case !errors.Is(err, ErrNotInKeychain):
			fmt.Fprintf(os.Stderr, "note: reading the keychain failed (%v); asking instead\n", err)
		}

		pass, err := next(prompt)
		if err != nil {
			return nil, err
		}

		if err := kc.Set(account, pass); err != nil {
			fmt.Fprintf(os.Stderr, "note: saving the passphrase to the keychain failed: %v\n", err)
		}
		cached = pass

		return pass, nil
	}
}

// SystemKeychainPrompt is KeychainPrompt over the platform keychain. Where there
// is none it says so on stderr and returns next unchanged.
func SystemKeychainPrompt(identityPath string, next func(string) ([]byte, error)) func(string) ([]byte, error) {
	kc, err := SystemKeychain()
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: keychain disabled: %v\n", err)

		return next
	}

	return KeychainPrompt(kc, identityPath, next)
}
