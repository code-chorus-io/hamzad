package store

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/1995parham/koochooloologin/internal/infra/crypt"
)

const (
	recipientsFile = "recipients.txt"
	secretsSubdir  = "secrets"
	bundleSuffix   = ".tar.age"
	// maxBundleEntry caps a single decompressed archive member to guard against
	// decompression bombs when unpacking an untrusted bundle.
	maxBundleEntry = 1 << 32 // 4 GiB
)

// ErrNoSecret is returned when a profile has no stored proxy secret.
var ErrNoSecret = errors.New("no secret stored for profile")

// errBundleEscape is returned when a bundle entry's path would escape the
// extraction directory (tar traversal).
var errBundleEscape = errors.New("bundle entry escapes the target directory")

// RecipientsPath is the path to the committed recipients file.
func (s *Store) RecipientsPath() string {
	return filepath.Join(s.Dir, recipientsFile)
}

func (s *Store) secretPath(name string) string {
	return filepath.Join(s.Dir, secretsSubdir, name+".age")
}

// BundlePath is the path to a profile's encrypted user-data-dir bundle.
func (s *Store) BundlePath(name string) string {
	return filepath.Join(s.Dir, dataDir, name+bundleSuffix)
}

// Recipients returns the configured recipient key lines.
func (s *Store) Recipients() ([]string, error) {
	f, err := os.Open(s.RecipientsPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening recipients: %w", err)
	}
	defer func() { _ = f.Close() }()

	var out []string
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("scanning recipients: %w", err)
	}

	return out, nil
}

// AddRecipient appends a recipient key (age1… or ssh-…) if it is not already
// present. It returns whether the key was newly added.
func (s *Store) AddRecipient(key string) (bool, error) {
	key = strings.TrimSpace(key)
	if !strings.HasPrefix(key, "age1") && !strings.HasPrefix(key, "ssh-") {
		return false, fmt.Errorf("%w: %q", crypt.ErrBadRecipient, key)
	}

	existing, err := s.Recipients()
	if err != nil {
		return false, err
	}
	if slices.Contains(existing, key) {
		return false, nil
	}

	if err := s.Ensure(); err != nil {
		return false, err
	}

	f, err := os.OpenFile(s.RecipientsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return false, fmt.Errorf("opening recipients for append: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := fmt.Fprintln(f, key); err != nil {
		return false, fmt.Errorf("writing recipient: %w", err)
	}

	return true, nil
}

// HasSecret reports whether a proxy secret is stored for the profile.
func (s *Store) HasSecret(name string) bool {
	_, err := os.Stat(s.secretPath(name))

	return err == nil
}

// SetProxy encrypts and stores a profile's proxy string. Encryption needs only
// the recipients, so this never requires the local identity.
func (s *Store) SetProxy(c crypt.Crypt, name, proxy string) error {
	if err := os.MkdirAll(filepath.Join(s.Dir, secretsSubdir), 0o750); err != nil {
		return fmt.Errorf("creating secrets dir: %w", err)
	}

	ciphertext, err := c.EncryptBytes([]byte(proxy))
	if err != nil {
		return err
	}

	if err := os.WriteFile(s.secretPath(name), ciphertext, 0o600); err != nil {
		return fmt.Errorf("writing secret: %w", err)
	}

	return nil
}

// Proxy decrypts and returns a profile's stored proxy string, or ErrNoSecret
// when none is stored. Decryption uses the local identity.
func (s *Store) Proxy(c crypt.Crypt, name string) (string, error) {
	ciphertext, err := os.ReadFile(s.secretPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %q", ErrNoSecret, name)
	}
	if err != nil {
		return "", fmt.Errorf("reading secret: %w", err)
	}

	plaintext, err := c.DecryptBytes(ciphertext)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(plaintext)), nil
}

func (s *Store) removeSecret(name string) error {
	err := os.Remove(s.secretPath(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing secret: %w", err)
	}

	return nil
}

// HasBundle reports whether an encrypted session bundle exists for the profile.
func (s *Store) HasBundle(name string) bool {
	_, err := os.Stat(s.BundlePath(name))

	return err == nil
}

// PackBundle archives the profile's working user-data directory as an encrypted
// tar.gz bundle. Encryption needs only the recipients.
func (s *Store) PackBundle(c crypt.Crypt, name string) error {
	dir := s.UserDataDir(name)
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("no working data for %q to pack: %w", name, err)
	}

	tmp, err := os.CreateTemp(filepath.Join(s.Dir, dataDir), "."+name+"-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp bundle: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := s.writeBundle(c, tmp, dir); err != nil {
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

func (s *Store) writeBundle(c crypt.Crypt, dst io.Writer, dir string) error {
	enc, err := c.EncryptWriter(dst)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(enc)
	tw := tar.NewWriter(gz)

	if err := tarDir(tw, dir); err != nil {
		return err
	}

	if err := tw.Close(); err != nil {
		return fmt.Errorf("closing tar: %w", err)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("closing gzip: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("closing age writer: %w", err)
	}

	return nil
}

// tarDir writes the regular files, directories, and symlinks under root into tw
// with paths relative to root. Irregular files (sockets, pipes) are skipped.
func tarDir(tw *tar.Writer, root string) error {
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		return tarEntry(tw, root, path, info)
	})
	if err != nil {
		return fmt.Errorf("walking %q: %w", root, err)
	}

	return nil
}

// tarEntry writes a single filesystem entry into tw, skipping the root itself
// and any irregular files (sockets, pipes, devices).
func tarEntry(tw *tar.Writer, root, path string, info os.FileInfo) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("relativizing %q: %w", path, err)
	}
	if rel == "." {
		return nil
	}

	link := ""
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		if link, err = os.Readlink(path); err != nil {
			return fmt.Errorf("reading symlink %q: %w", path, err)
		}
	case !info.Mode().IsRegular() && !info.IsDir():
		return nil
	}

	header, err := tar.FileInfoHeader(info, link)
	if err != nil {
		return fmt.Errorf("tar header for %q: %w", path, err)
	}
	header.Name = filepath.ToSlash(rel)

	if err := tw.WriteHeader(header); err != nil {
		return fmt.Errorf("writing tar header: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	return copyFileInto(tw, path)
}

func copyFileInto(tw *tar.Writer, path string) error {
	f, err := os.Open(path) //nolint:gosec // path comes from filepath.Walk over our own store dir

	if err != nil {
		return fmt.Errorf("opening %q: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := io.Copy(tw, f); err != nil {
		return fmt.Errorf("archiving %q: %w", path, err)
	}

	return nil
}

// UnpackBundle decrypts and extracts a profile's bundle over its working
// user-data directory, replacing it. Decryption uses the local identity.
func (s *Store) UnpackBundle(c crypt.Crypt, name string) error {
	f, err := os.Open(s.BundlePath(name))
	if err != nil {
		return fmt.Errorf("opening bundle: %w", err)
	}
	defer func() { _ = f.Close() }()

	dec, err := c.DecryptReader(f)
	if err != nil {
		return err
	}
	gz, err := gzip.NewReader(dec)
	if err != nil {
		return fmt.Errorf("opening gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()

	dir := s.UserDataDir(name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing working dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating working dir: %w", err)
	}

	return extractTar(tar.NewReader(gz), dir)
}

func extractTar(tr *tar.Reader, dir string) error {
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		target, err := safeJoin(dir, header.Name)
		if err != nil {
			return err
		}

		if err := extractEntry(tr, header, target); err != nil {
			return err
		}
	}
}

func extractEntry(tr *tar.Reader, header *tar.Header, target string) error {
	switch header.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(target, 0o750); err != nil {
			return fmt.Errorf("creating dir %q: %w", target, err)
		}
	case tar.TypeSymlink:
		if err := os.Symlink(header.Linkname, target); err != nil {
			return fmt.Errorf("creating symlink %q: %w", target, err)
		}
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return fmt.Errorf("creating parent of %q: %w", target, err)
		}
		if err := writeRegular(tr, target, header.FileInfo().Mode().Perm()); err != nil {
			return err
		}
	default:
		// Ignore unsupported entry types rather than fail the whole extract.
	}

	return nil
}

func writeRegular(tr *tar.Reader, target string, mode os.FileMode) error {
	//nolint:gosec // target is validated by safeJoin against directory traversal
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return fmt.Errorf("creating %q: %w", target, err)
	}
	defer func() { _ = out.Close() }()

	if _, err := io.CopyN(out, tr, maxBundleEntry); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("writing %q: %w", target, err)
	}

	return nil
}

// safeJoin joins name onto dir, rejecting paths that would escape dir (a
// zip-slip / tar traversal guard).
func safeJoin(dir, name string) (string, error) {
	target := filepath.Join(dir, filepath.FromSlash(name))
	clean := filepath.Clean(target)
	if clean != dir && !strings.HasPrefix(clean, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q", errBundleEscape, name)
	}

	return clean, nil
}

func (s *Store) removeBundle(name string) error {
	err := os.Remove(s.BundlePath(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing bundle: %w", err)
	}

	return nil
}
