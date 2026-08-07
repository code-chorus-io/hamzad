package browser

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// errBadStatus is returned when an HTTP fetch answers with a non-200 status.
var errBadStatus = errors.New("unexpected status")

// errArchiveEscape is returned when an archive entry's path would escape the
// extraction directory (zip slip).
var errArchiveEscape = errors.New("archive entry escapes the target directory")

// errEntryTooLarge is returned when an archive member exceeds maxEntry.
var errEntryTooLarge = errors.New("archive entry is too large")

// maxEntry caps a single decompressed member. Chrome's own binary is around
// 300 MB, so the guard against a decompression bomb has to sit well above that.
const maxEntry = 1 << 30 // 1 GiB

// Install downloads and extracts a build, returning its metadata. An install
// that is already present is returned as-is without touching the network.
//
// progress, when non-nil, is called with the bytes fetched so far and the total
// (or 0 when the server declines to say), so a caller can render a bar.
func Install(ctx context.Context, build Build, progress func(done, total int64)) (Metadata, error) {
	if meta, err := readInstalled(build.Version); err == nil {
		return meta, nil
	}

	dir, err := versionDir(build.Version)
	if err != nil {
		return Metadata{}, err
	}

	// Extract into a sibling temp dir and rename into place, so an interrupted
	// install never leaves a directory that looks complete.
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return Metadata{}, fmt.Errorf("creating browser cache: %w", err)
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "."+build.Version+"-*")
	if err != nil {
		return Metadata{}, fmt.Errorf("creating temp install dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	sum, err := fetchAndExtract(ctx, build, tmp, progress)
	if err != nil {
		return Metadata{}, err
	}

	meta := Metadata{
		Version:   build.Version,
		Platform:  build.Platform,
		URL:       build.URL,
		SHA256:    sum,
		Installed: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeMetadata(tmp, meta); err != nil {
		return Metadata{}, err
	}

	if err := os.Rename(tmp, dir); err != nil {
		return Metadata{}, fmt.Errorf("publishing install: %w", err)
	}

	return meta, nil
}

// readInstalled returns the metadata of a complete existing install.
func readInstalled(version string) (Metadata, error) {
	dir, err := versionDir(version)
	if err != nil {
		return Metadata{}, err
	}

	meta, err := readMetadata(dir)
	if err != nil {
		return Metadata{}, err
	}
	if _, err := os.Stat(filepath.Join(dir, execRelPath(meta.Platform))); err != nil {
		return Metadata{}, fmt.Errorf("%w: %q is incomplete", ErrNotInstalled, version)
	}

	return meta, nil
}

// fetchAndExtract streams the archive to a temp file, hashing as it goes, then
// unpacks it into dest. It returns the archive's SHA-256.
//
// The hash is recorded rather than checked: Chrome for Testing publishes no
// checksums, so there is nothing to verify against on first download and the
// trust anchor is TLS to Google's bucket. Recording it gives later installs and
// teammates a fixed digest to compare, which is the part that can be verified.
func fetchAndExtract(ctx context.Context, build Build, dest string, progress func(done, total int64)) (string, error) {
	archive, err := os.CreateTemp("", "hamzad-chrome-*.zip")
	if err != nil {
		return "", fmt.Errorf("creating temp archive: %w", err)
	}
	defer func() {
		_ = archive.Close()
		_ = os.Remove(archive.Name())
	}()

	sum, size, err := download(ctx, build.URL, archive, progress)
	if err != nil {
		return "", err
	}

	zr, err := zip.NewReader(archive, size)
	if err != nil {
		return "", fmt.Errorf("opening archive: %w", err)
	}
	if err := extractZip(zr, dest, maxEntry); err != nil {
		return "", err
	}

	return sum, nil
}

// download streams url into dst, returning the content's SHA-256 and length.
func download(ctx context.Context, url string, dst *os.File, progress func(done, total int64)) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, fmt.Errorf("building download request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("downloading browser: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("downloading browser: %w: %s", errBadStatus, resp.Status)
	}

	hash := sha256.New()
	counter := &progressWriter{total: resp.ContentLength, report: progress}

	size, err := io.Copy(dst, io.TeeReader(resp.Body, io.MultiWriter(hash, counter)))
	if err != nil {
		return "", 0, fmt.Errorf("saving browser archive: %w", err)
	}

	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

// progressWriter counts bytes and forwards the running total to a reporter.
type progressWriter struct {
	done   int64
	total  int64
	report func(done, total int64)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	w.done += int64(len(p))
	if w.report != nil {
		w.report(w.done, w.total)
	}

	return len(p), nil
}

func extractZip(zr *zip.Reader, dest string, limit int64) error {
	for _, f := range zr.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if err := extractEntry(f, target, limit); err != nil {
			return err
		}
	}

	return nil
}

// extractEntry unpacks one archive member, preserving its mode so the browser
// binary stays executable.
func extractEntry(f *zip.File, target string, limit int64) error {
	info := f.FileInfo()

	switch {
	case info.IsDir():
		if err := os.MkdirAll(target, 0o750); err != nil {
			return fmt.Errorf("creating dir %q: %w", target, err)
		}

		return nil
	case info.Mode()&os.ModeSymlink != 0:
		// macOS .app bundles are full of symlinks; Linux and Windows have none.
		return extractSymlink(f, target)
	default:
		return extractFile(f, target, info.Mode().Perm(), limit)
	}
}

func extractFile(f *zip.File, target string, mode os.FileMode, limit int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("creating parent of %q: %w", target, err)
	}

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("reading %q from archive: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	//nolint:gosec // target is validated by safeJoin against archive traversal
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("creating %q: %w", target, err)
	}
	defer func() { _ = out.Close() }()

	// One byte past the cap distinguishes an oversized entry from one that ends
	// exactly on it, so a bomb fails loudly instead of extracting truncated.
	n, err := io.CopyN(out, rc, limit+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("extracting %q: %w", target, err)
	}
	if n > limit {
		return fmt.Errorf("%w: %q exceeds %d bytes", errEntryTooLarge, f.Name, limit)
	}

	return nil
}

func extractSymlink(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("reading symlink %q: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	raw, err := io.ReadAll(io.LimitReader(rc, 4096))
	if err != nil {
		return fmt.Errorf("reading symlink target of %q: %w", f.Name, err)
	}
	link := string(raw)

	// An absolute or upward link could be followed by a later entry to write
	// outside dest, which safeJoin alone cannot catch since it is only lexical.
	if filepath.IsAbs(link) {
		return fmt.Errorf("%w: symlink %q -> %q", errArchiveEscape, f.Name, link)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return fmt.Errorf("creating parent of %q: %w", target, err)
	}

	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), link))
	root := filepath.Clean(rootOf(target, f.Name))
	if resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
		return fmt.Errorf("%w: symlink %q -> %q", errArchiveEscape, f.Name, link)
	}

	if err := os.Symlink(link, target); err != nil {
		return fmt.Errorf("creating symlink %q: %w", target, err)
	}

	return nil
}

// rootOf recovers the extraction root from a target path and the archive-
// relative name that produced it.
func rootOf(target, name string) string {
	return strings.TrimSuffix(filepath.Clean(target), string(os.PathSeparator)+filepath.FromSlash(filepath.Clean(name)))
}

// safeJoin joins name onto dir, rejecting paths that would escape dir.
func safeJoin(dir, name string) (string, error) {
	target := filepath.Clean(filepath.Join(dir, filepath.FromSlash(name)))
	if target != dir && !strings.HasPrefix(target, dir+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: %q", errArchiveEscape, name)
	}

	return target, nil
}
