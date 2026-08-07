package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
)

// ReencryptKind names the two encrypted artifact types a store holds.
type ReencryptKind string

const (
	// SecretKind is a profile's encrypted proxy, secrets/<name>.age.
	SecretKind ReencryptKind = "secret"
	// BundleKind is a profile's encrypted session bundle, data/<name>.tar.age.
	BundleKind ReencryptKind = "bundle"
)

// Reencrypted is the outcome for a single artifact. Err is nil when the
// artifact was rewritten to the current recipients.
type Reencrypted struct {
	Kind ReencryptKind
	Name string
	Err  error
}

// Reencrypt rewrites every stored secret and session bundle so that it is
// encrypted to the recipients currently listed in recipients.txt.
//
// age fixes the recipient set at encryption time, so adding a key to
// recipients.txt leaves everything already written unreadable to it — this is
// what makes a newly added teammate able to see profiles.toml yet unable to
// open a single profile. It is the one store operation that needs both halves
// of the key material: the local identity to read the old ciphertext, and the
// recipient list to write the new one.
//
// An artifact the local identity cannot decrypt is reported in the result and
// left untouched rather than aborting the run. After a rotation some artifacts
// are commonly unreadable to the person doing the re-encrypt, and the ones that
// can be brought forward still should be.
func (s *Store) Reencrypt(c crypt.Crypt) ([]Reencrypted, error) {
	if !c.Configured() {
		return nil, crypt.ErrNoRecipients
	}

	secrets, err := s.encryptedNames(secretsSubdir, secretSuffix)
	if err != nil {
		return nil, err
	}
	bundles, err := s.encryptedNames(dataDir, bundleSuffix)
	if err != nil {
		return nil, err
	}

	out := make([]Reencrypted, 0, len(secrets)+len(bundles))
	for _, name := range secrets {
		out = append(out, Reencrypted{Kind: SecretKind, Name: name, Err: s.reencryptSecret(c, name)})
	}
	for _, name := range bundles {
		out = append(out, Reencrypted{Kind: BundleKind, Name: name, Err: s.reencryptBundle(c, name)})
	}

	return out, nil
}

// encryptedNames returns the profile names behind the files in the store's sub
// directory whose names end in suffix, sorted. A missing directory yields no
// names, so an untouched store is simply empty rather than an error.
func (s *Store) encryptedNames(sub, suffix string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, sub))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s dir: %w", sub, err)
	}

	var names []string
	for _, e := range entries {
		// data/ holds the unencrypted working copies next to the bundles, and
		// both directories can hold a half-written .tmp from an interrupted save.
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		name, ok := strings.CutSuffix(e.Name(), suffix)
		if !ok || name == "" {
			continue
		}
		names = append(names, name)
	}
	slices.Sort(names)

	return names, nil
}

// reencryptSecret decrypts and re-encrypts one profile's proxy in place.
func (s *Store) reencryptSecret(c crypt.Crypt, name string) error {
	ciphertext, err := os.ReadFile(s.secretPath(name))
	if err != nil {
		return fmt.Errorf("reading secret: %w", err)
	}

	plaintext, err := c.DecryptBytes(ciphertext)
	if err != nil {
		return err
	}

	fresh, err := c.EncryptBytes(plaintext)
	if err != nil {
		return err
	}

	return replaceFile(s.secretPath(name), fresh)
}

// reencryptBundle rewrites one profile's session bundle. The gzip and tar
// layers are streamed through untouched — only the age envelope is replaced —
// so a multi-hundred-megabyte bundle never has to be unpacked to disk.
func (s *Store) reencryptBundle(c crypt.Crypt, name string) error {
	src, err := os.Open(s.BundlePath(name))
	if err != nil {
		return fmt.Errorf("opening bundle: %w", err)
	}
	defer func() { _ = src.Close() }()

	dec, err := c.DecryptReader(src)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Join(s.Dir, dataDir), "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp bundle: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := copyEncrypted(c, tmp, dec); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp bundle: %w", err)
	}

	if err := os.Rename(tmp.Name(), s.BundlePath(name)); err != nil {
		return fmt.Errorf("replacing bundle: %w", err)
	}

	return nil
}

// copyEncrypted streams src into dst through a fresh age writer.
func copyEncrypted(c crypt.Crypt, dst io.Writer, src io.Reader) error {
	enc, err := c.EncryptWriter(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(enc, src); err != nil {
		return fmt.Errorf("re-encrypting bundle: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("closing age writer: %w", err)
	}

	return nil
}

// replaceFile atomically replaces path with data, writing a temp file beside it
// first. A secret that is truncated mid-write is gone for good — nothing else in
// the store can reconstruct it — so the rename is worth the extra file.
func replaceFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("writing %q: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %q: %w", tmp.Name(), err)
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing %q: %w", path, err)
	}

	return nil
}
