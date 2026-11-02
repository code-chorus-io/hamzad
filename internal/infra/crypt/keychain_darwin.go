//go:build darwin

package crypt

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// securityPath is macOS's keychain CLI. hamzad builds without cgo, so it cannot
// link Security.framework and talks to the login keychain through this instead.
const securityPath = "/usr/bin/security"

// keychainService is the service name hamzad files its Keychain items under.
// The account is the identity's absolute path, so each key has its own item.
const keychainService = "hamzad"

// securityItemNotFound is the exit status `security` uses for a missing item.
const securityItemNotFound = 44

// macKeychain keeps passphrases as generic passwords in the login keychain.
type macKeychain struct{}

// SystemKeychain returns the macOS login keychain.
func SystemKeychain() (Keychain, error) {
	return macKeychain{}, nil
}

// Get reads the passphrase stored for account.
func (macKeychain) Get(account string) ([]byte, error) {
	cmd := exec.Command(securityPath, "find-generic-password", "-s", keychainService, "-a", account, "-w") //nolint:gosec,noctx // fixed binary; may wait on a keychain unlock dialog
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == securityItemNotFound {
			return nil, ErrNotInKeychain
		}

		return nil, fmt.Errorf("security find-generic-password: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	// -w prints the password and a newline; anything else in it is the password.
	return bytes.TrimSuffix(out, []byte("\n")), nil
}

// Set adds or updates the item. It runs `security -i` and writes the command on
// stdin, the passphrase hex-encoded with -X, so the passphrase never appears in
// an argument list where `ps` could read it, and needs no quoting.
func (macKeychain) Set(account string, passphrase []byte) error {
	if strings.ContainsAny(account, "\"\\\n") {
		return fmt.Errorf("%w: %q", ErrBadKeychainAccount, account)
	}

	line := fmt.Sprintf("add-generic-password -U -s %s -a \"%s\" -l \"hamzad: %s\" -X %s\n",
		keychainService, account, account, hex.EncodeToString(passphrase))

	cmd := exec.Command(securityPath, "-i") //nolint:noctx // fixed binary; may wait on a keychain unlock dialog
	cmd.Stdin = strings.NewReader(line)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("security add-generic-password: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	return nil
}
