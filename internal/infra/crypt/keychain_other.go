//go:build !darwin

package crypt

// SystemKeychain reports that this platform has no supported keychain.
func SystemKeychain() (Keychain, error) {
	return nil, ErrKeychainUnsupported
}
