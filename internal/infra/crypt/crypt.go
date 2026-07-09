// Package crypt encrypts and decrypts store data with age, using the team's
// recipients (age or SSH public keys) to encrypt and the local user's identity
// (an age key or SSH private key) to decrypt. Encryption needs only public
// recipients; only decryption touches the private identity.
package crypt

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"filippo.io/age"
	"filippo.io/age/agessh"
	"filippo.io/age/armor"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// ErrNoRecipients is returned when encryption is attempted but no recipients
// are configured.
var ErrNoRecipients = errors.New("no recipients configured; run 'store init' to add your key")

// ErrBadRecipient is returned for a recipient line that is neither an age nor
// an SSH public key.
var ErrBadRecipient = errors.New("unrecognized recipient (want age1… or ssh-…)")

// errNoIdentities is returned when a key file yields no usable age identity.
var errNoIdentities = errors.New("no identities in key file")

// Crypt encrypts to recipients read from RecipientsPath and decrypts with the
// identity at IdentityPath.
type Crypt struct {
	RecipientsPath string
	IdentityPath   string
	// Prompt reads a passphrase for an encrypted SSH identity. When nil, the
	// controlling terminal is used.
	Prompt func(prompt string) ([]byte, error)
}

// New returns a Crypt reading recipients and identity from the given paths.
func New(recipientsPath, identityPath string) Crypt {
	return Crypt{RecipientsPath: recipientsPath, IdentityPath: identityPath}
}

// Configured reports whether at least one recipient is available, i.e. whether
// the store is set up for encryption.
func (c Crypt) Configured() bool {
	rs, err := c.recipients()

	return err == nil && len(rs) > 0
}

// EncryptBytes returns plaintext encrypted to the configured recipients as
// ASCII-armored age text, suitable for small, diff-friendly secret files.
func (c Crypt) EncryptBytes(plaintext []byte) ([]byte, error) {
	var buf bytes.Buffer

	armorW := armor.NewWriter(&buf)
	w, err := c.encryptWriter(armorW)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("writing ciphertext: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("closing age writer: %w", err)
	}
	if err := armorW.Close(); err != nil {
		return nil, fmt.Errorf("closing armor writer: %w", err)
	}

	return buf.Bytes(), nil
}

// DecryptBytes decrypts ASCII-armored age text produced by EncryptBytes.
func (c Crypt) DecryptBytes(ciphertext []byte) ([]byte, error) {
	id, err := c.identity()
	if err != nil {
		return nil, err
	}

	r, err := age.Decrypt(armor.NewReader(bytes.NewReader(ciphertext)), id)
	if err != nil {
		return nil, fmt.Errorf("decrypting: %w", err)
	}

	out, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("reading plaintext: %w", err)
	}

	return out, nil
}

// EncryptWriter wraps dst so that everything written to the returned closer is
// age-encrypted (binary, unarmored — meant for large bundle streams).
func (c Crypt) EncryptWriter(dst io.Writer) (io.WriteCloser, error) {
	return c.encryptWriter(dst)
}

// DecryptReader wraps src so that reads yield the decrypted stream.
func (c Crypt) DecryptReader(src io.Reader) (io.Reader, error) {
	id, err := c.identity()
	if err != nil {
		return nil, err
	}

	r, err := age.Decrypt(src, id)
	if err != nil {
		return nil, fmt.Errorf("decrypting stream: %w", err)
	}

	return r, nil
}

func (c Crypt) encryptWriter(dst io.Writer) (io.WriteCloser, error) {
	rs, err := c.recipients()
	if err != nil {
		return nil, err
	}
	if len(rs) == 0 {
		return nil, ErrNoRecipients
	}

	w, err := age.Encrypt(dst, rs...)
	if err != nil {
		return nil, fmt.Errorf("creating age writer: %w", err)
	}

	return w, nil
}

// recipients parses the recipients file into age recipients. age public keys
// (age1…) and SSH public keys (ssh-ed25519…, ssh-rsa…) are both accepted.
func (c Crypt) recipients() ([]age.Recipient, error) {
	lines, err := readRecipientLines(c.RecipientsPath)
	if err != nil {
		return nil, fmt.Errorf("reading recipients: %w", err)
	}

	var out []age.Recipient
	for _, line := range lines {
		r, err := parseRecipient(line)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}

	return out, nil
}

// readRecipientLines returns the non-empty, non-comment lines of the recipients
// file. A missing file yields no lines rather than an error, so an unconfigured
// store simply has no recipients.
func readRecipientLines(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // recipients file path is operator-configured, not attacker input
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	var lines []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("scanning %q: %w", path, err)
	}

	return lines, nil
}

func parseRecipient(line string) (age.Recipient, error) {
	switch {
	case strings.HasPrefix(line, "age1"):
		r, err := age.ParseX25519Recipient(line)
		if err != nil {
			return nil, fmt.Errorf("parsing age recipient: %w", err)
		}

		return r, nil
	case strings.HasPrefix(line, "ssh-"):
		r, err := agessh.ParseRecipient(line)
		if err != nil {
			return nil, fmt.Errorf("parsing ssh recipient: %w", err)
		}

		return r, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrBadRecipient, line)
	}
}

// identity loads the local decryption identity: a native age key file or an
// SSH private key, prompting for a passphrase when the SSH key is encrypted.
func (c Crypt) identity() (age.Identity, error) {
	pem, err := os.ReadFile(c.IdentityPath)
	if err != nil {
		return nil, fmt.Errorf("reading identity %q: %w", c.IdentityPath, err)
	}

	if bytes.Contains(pem, []byte("AGE-SECRET-KEY-")) {
		ids, err := age.ParseIdentities(bytes.NewReader(pem))
		if err != nil {
			return nil, fmt.Errorf("parsing age identity: %w", err)
		}
		if len(ids) == 0 {
			return nil, fmt.Errorf("%w: %q", errNoIdentities, c.IdentityPath)
		}

		return ids[0], nil
	}

	id, err := agessh.ParseIdentity(pem)
	if err == nil {
		return id, nil
	}

	if missing, ok := errors.AsType[*ssh.PassphraseMissingError](err); ok {
		return c.encryptedSSHIdentity(pem, missing)
	}

	return nil, fmt.Errorf("parsing ssh identity %q: %w", c.IdentityPath, err)
}

// encryptedSSHIdentity builds an identity for a passphrase-protected SSH key,
// sourcing the public key from the error or the sibling .pub file.
func (c Crypt) encryptedSSHIdentity(pem []byte, missing *ssh.PassphraseMissingError) (age.Identity, error) {
	pub := missing.PublicKey
	if pub == nil {
		pubBytes, err := os.ReadFile(c.IdentityPath + ".pub")
		if err != nil {
			return nil, fmt.Errorf("encrypted key needs its %s.pub: %w", c.IdentityPath, err)
		}
		pub, _, _, _, err = ssh.ParseAuthorizedKey(pubBytes)
		if err != nil {
			return nil, fmt.Errorf("parsing %s.pub: %w", c.IdentityPath, err)
		}
	}

	prompt := func() ([]byte, error) {
		return c.askPassphrase(fmt.Sprintf("passphrase for %s: ", c.IdentityPath))
	}

	id, err := agessh.NewEncryptedSSHIdentity(pub, pem, prompt)
	if err != nil {
		return nil, fmt.Errorf("preparing encrypted ssh identity: %w", err)
	}

	return id, nil
}

func (c Crypt) askPassphrase(prompt string) ([]byte, error) {
	if c.Prompt != nil {
		return c.Prompt(prompt)
	}

	fmt.Fprint(os.Stderr, prompt)
	pass, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("reading passphrase: %w", err)
	}

	return pass, nil
}
