package browser //nolint:testpackage // white-box: exercises the unexported archive extractor and version sort

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// supportedPlatforms is every Chrome for Testing platform key the installer
// claims to handle. linux-arm64 is deliberately absent: Google publishes none.
var supportedPlatforms = []string{
	platformLinux64, platformMacX64, platformMacARM64, platformWin32, platformWin64,
}

// testVersion is a real published Chrome for Testing build, used wherever an
// exact version is needed.
const testVersion = "151.0.7922.76"

// TestExecRelPathCoversEveryPlatform guards the mapping that turns an extracted
// archive into a runnable binary. A wrong path here does not fail the install —
// the download and extraction both succeed — it fails much later, at launch,
// looking like a corrupt install.
func TestExecRelPathCoversEveryPlatform(t *testing.T) {
	t.Parallel()

	for _, p := range supportedPlatforms {
		rel := execRelPath(p)
		if rel == "" {
			t.Errorf("no executable path for platform %q", p)

			continue
		}
		// Every archive nests its payload under one directory named for the
		// platform, so the mapping must start there.
		if !strings.HasPrefix(filepath.ToSlash(rel), "chrome-"+p+"/") {
			t.Errorf("platform %q: exec path %q does not start with chrome-%s/", p, rel, p)
		}
	}

	if got := execRelPath("linux-arm64"); got != "" {
		t.Errorf("unsupported platform should map to no path, got %q", got)
	}
}

// TestPlatformRejectsUnpublishedTargets checks the host mapping resolves to one
// of the published keys, or fails with a usable error. linux/arm64 is the real
// case: we ship a linux_arm64 binary of the CLI itself, and those users must be
// told to bring their own browser rather than watch a download 404.
func TestPlatformRejectsUnpublishedTargets(t *testing.T) {
	t.Parallel()

	got, err := Platform()
	if err != nil {
		if !strings.Contains(err.Error(), "--chrome-path") {
			t.Errorf("unsupported-platform error should suggest --chrome-path, got %v", err)
		}

		return
	}

	if !slices.Contains(supportedPlatforms, got) {
		t.Errorf("Platform() = %q, not a published Chrome for Testing key", got)
	}
}

// TestLessVersionOrdersNumerically pins the sort used to pick the newest
// install. A lexical compare puts 9.x after 151.x, which would silently launch
// an old browser as the "newest" one.
func TestLessVersionOrdersNumerically(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"9.0.0.0", "10.0.0.0", true},
		{"99.0.1.1", testVersion, true},
		{testVersion, "151.0.7922.9", false},
		{testVersion, testVersion, false},
		{"151.0.7922", "151.0.7922.1", true},
	} {
		if got := lessVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("lessVersion(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// zipEntry describes one member to build into a test archive.
type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

func buildZip(t *testing.T, entries []zipEntry) *zip.Reader {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		header.SetMode(e.mode)
		w, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatalf("creating entry %q: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatalf("writing entry %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("closing zip: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reading back zip: %v", err)
	}

	return zr
}

// TestExtractZipPreservesExecutableBit is the one that makes the feature work:
// the archive stores Chrome's mode as 0755, and an extractor that drops it
// produces an install that downloads cleanly and then cannot be launched.
func TestExtractZipPreservesExecutableBit(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	zr := buildZip(t, []zipEntry{
		{name: "chrome-" + platformLinux64 + "/chrome", body: "#!/bin/true\n", mode: 0o755},
		{name: "chrome-" + platformLinux64 + "/product_logo.png", body: "png", mode: 0o644},
	})

	if err := extractZip(zr, dest, maxEntry); err != nil {
		t.Fatalf("extractZip: %v", err)
	}

	info, err := os.Stat(filepath.Join(dest, "chrome-"+platformLinux64, "chrome"))
	if err != nil {
		t.Fatalf("stat extracted binary: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("extracted browser is not executable (mode %v)", info.Mode().Perm())
	}

	plain, err := os.Stat(filepath.Join(dest, "chrome-"+platformLinux64, "product_logo.png"))
	if err != nil {
		t.Fatalf("stat extracted asset: %v", err)
	}
	if plain.Mode().Perm()&0o111 != 0 {
		t.Errorf("non-executable asset came out executable (mode %v)", plain.Mode().Perm())
	}
}

// TestExtractZipContainsHostileNames covers zip-slip (CWE-022): a hostile
// archive naming ../../ must not write outside the extraction directory.
//
// The invariant asserted is containment, not rejection. A "/abs/escape" entry is
// legitimately allowed through because filepath.Join normalizes it to
// dest/abs/escape, which is inside — refusing it would be a nicety, but writing
// it anywhere else would be the actual vulnerability. So this checks the parent
// directory stays empty however the extractor chooses to handle each name.
func TestExtractZipContainsHostileNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"../escape", "chrome-" + platformLinux64 + "/../../escape", "/abs/escape", "../../../../etc/pwned"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			if err := os.MkdirAll(dest, 0o750); err != nil {
				t.Fatalf("creating dest: %v", err)
			}

			zr := buildZip(t, []zipEntry{{name: name, body: "pwned", mode: 0o644}})
			_ = extractZip(zr, dest, maxEntry) // an error is fine; escaping is not

			entries, err := os.ReadDir(parent)
			if err != nil {
				t.Fatalf("reading parent: %v", err)
			}
			for _, e := range entries {
				if e.Name() != "dest" {
					t.Errorf("entry %q escaped the extraction dir: wrote %q beside it", name, e.Name())
				}
			}
		})
	}
}

// TestExtractZipRejectsOversizedEntry keeps a decompression bomb from filling
// the disk. The real cap is 1 GiB, which is why the limit is a parameter.
func TestExtractZipRejectsOversizedEntry(t *testing.T) {
	t.Parallel()

	dest := t.TempDir()
	zr := buildZip(t, []zipEntry{{name: "chrome-" + platformLinux64 + "/big", body: strings.Repeat("A", 512), mode: 0o644}})

	err := extractZip(zr, dest, 64)
	if err == nil {
		t.Fatal("extractZip accepted an entry past the cap")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error should name the size problem, got %v", err)
	}
}

// TestResolvePinnedVersionSkipsTheNetwork checks an exact version derives its
// URL locally. Only a channel name needs the manifest, so pinning stays usable
// offline and in a sandboxed CI.
func TestResolvePinnedVersionSkipsTheNetwork(t *testing.T) {
	t.Parallel()

	if _, err := Platform(); err != nil {
		t.Skip("no Chrome for Testing build for this platform")
	}

	build, err := Resolve(context.Background(), testVersion)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if build.Version != testVersion {
		t.Errorf("version = %q", build.Version)
	}
	if !strings.HasPrefix(build.URL, "https://") {
		t.Errorf("download must use TLS, got %q", build.URL)
	}
	if !strings.Contains(build.URL, testVersion+"/"+build.Platform+"/") {
		t.Errorf("URL %q does not address the pinned version and platform", build.URL)
	}
}

// TestExecPathReportsMissingInstall keeps a never-installed version from
// resolving to a path that does not exist.
func TestExecPathReportsMissingInstall(t *testing.T) {
	t.Parallel()

	if _, err := ExecPath("0.0.0.0-definitely-absent"); err == nil {
		t.Fatal("ExecPath returned a path for a version that was never installed")
	}
}
