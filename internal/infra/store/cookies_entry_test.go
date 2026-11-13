package store_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// TestBundleCarriesCookiesInMemory checks that exported cookies ride inside the
// encrypted bundle and come back from UnpackBundleWith without ever being
// written into the working directory in plaintext.
func TestBundleCarriesCookiesInMemory(t *testing.T) {
	t.Parallel()

	st, c := newCrypt(t)

	dir := st.UserDataDir("work")
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "Default", "Preferences"), `{}`)
	// A stray plaintext copy, as an older hamzad would leave behind. It must
	// not be packed next to (and shadow) the fresh export.
	writeFile(t, filepath.Join(dir, store.CookiesEntry), "stale")

	const cookies = `{"format":1,"cookies":[{"name":"sid","value":"secret"}]}`
	if err := st.PackBundleWith(c, "work", []byte(cookies)); err != nil {
		t.Fatalf("PackBundleWith: %v", err)
	}

	got, err := st.UnpackBundleWith(c, "work")
	if err != nil {
		t.Fatalf("UnpackBundleWith: %v", err)
	}
	if string(got) != cookies {
		t.Fatalf("cookies = %q, want %q", got, cookies)
	}
	if _, err := os.Stat(filepath.Join(dir, store.CookiesEntry)); !os.IsNotExist(err) {
		t.Fatalf("cookies were written to the working directory (stat err = %v)", err)
	}
	if got := readFile(t, filepath.Join(dir, "Default", "Preferences")); got != `{}` {
		t.Fatalf("Preferences = %q", got)
	}
}

// TestBundleWithoutCookies checks that a bundle packed without an export (every
// bundle written before this feature) unpacks with no cookies and no error.
func TestBundleWithoutCookies(t *testing.T) {
	t.Parallel()

	st, c := newCrypt(t)

	dir := st.UserDataDir("work")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "Local State"), `{}`)

	if err := st.PackBundle(c, "work"); err != nil {
		t.Fatalf("PackBundle: %v", err)
	}

	got, err := st.UnpackBundleWith(c, "work")
	if err != nil {
		t.Fatalf("UnpackBundleWith: %v", err)
	}
	if got != nil {
		t.Fatalf("cookies = %q, want nil", got)
	}
}
