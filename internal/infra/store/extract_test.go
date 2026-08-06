package store //nolint:testpackage // white-box: exercises the unexported extraction size guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteRegularRejectsOversizedEntry is the regression guard for a silent
// truncation. The extractor copied at most maxBundleEntry bytes and treated a
// short copy as success, so an entry past the cap was written as a prefix of
// itself and the restore reported no error — handing Chrome a half-file that
// looks like corruption rather than a rejected bundle. The real cap is 4 GiB,
// which is why the limit is a parameter: the behavior is testable at any size.
func TestWriteRegularRejectsOversizedEntry(t *testing.T) {
	t.Parallel()

	target := filepath.Join(t.TempDir(), "big")

	err := writeRegular(strings.NewReader("0123456789abcdef"), target, 0o600, 8)
	if err == nil {
		t.Fatal("an entry past the cap must be rejected, not truncated")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("error should name the size problem, got %v", err)
	}
}

// TestWriteRegularAcceptsEntryAtLimit checks the boundary is inclusive, so a
// file exactly at the cap still restores.
func TestWriteRegularAcceptsEntryAtLimit(t *testing.T) {
	t.Parallel()

	const content = "12345678"

	target := filepath.Join(t.TempDir(), "exact")
	if err := writeRegular(strings.NewReader(content), target, 0o600, int64(len(content))); err != nil {
		t.Fatalf("an entry exactly at the cap must be accepted: %v", err)
	}

	got, err := os.ReadFile(target) //nolint:gosec // test reads a path it just wrote
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != content {
		t.Errorf("extracted %q, want %q", got, content)
	}
}
