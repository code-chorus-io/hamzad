package store_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/1995parham/koochooloologin/internal/infra/crypt"
	"github.com/1995parham/koochooloologin/internal/infra/store"
)

// newCrypt sets up a store with a fresh age identity as its sole recipient and
// returns the store plus a crypt bound to it.
func newCrypt(t *testing.T) (*store.Store, crypt.Crypt) {
	t.Helper()

	st := store.New(t.TempDir())
	if err := st.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	if err := os.WriteFile(st.RecipientsPath(), []byte(id.Recipient().String()+"\n"), 0o600); err != nil {
		t.Fatalf("write recipients: %v", err)
	}
	idPath := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(idPath, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}

	return st, crypt.New(st.RecipientsPath(), idPath)
}

func TestSecretRoundTrip(t *testing.T) {
	t.Parallel()

	st, c := newCrypt(t)

	if !c.Configured() {
		t.Fatal("expected crypt to be configured")
	}
	if st.HasSecret("work") {
		t.Fatal("unexpected pre-existing secret")
	}

	const proxy = "socks5://user:pass@1.2.3.4:1080"
	if err := st.SetProxy(c, "work", proxy); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}
	if !st.HasSecret("work") {
		t.Fatal("expected secret after SetProxy")
	}

	// The stored secret file must not contain the plaintext proxy.
	raw, err := os.ReadFile(filepath.Join(st.Dir, "secrets", "work.age"))
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if bytes.Contains(raw, []byte(proxy)) || bytes.Contains(raw, []byte("pass")) {
		t.Fatal("secret file leaked plaintext")
	}

	got, err := st.Proxy(c, "work")
	if err != nil {
		t.Fatalf("Proxy: %v", err)
	}
	if got != proxy {
		t.Fatalf("Proxy = %q, want %q", got, proxy)
	}
}

func TestBundleRoundTrip(t *testing.T) {
	t.Parallel()

	st, c := newCrypt(t)

	dir := st.UserDataDir("work")
	if err := os.MkdirAll(filepath.Join(dir, "Default"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "Cookies"), "cookie-jar")
	writeFile(t, filepath.Join(dir, "Default", "Preferences"), `{"k":"v"}`)

	if err := st.PackBundle(c, "work"); err != nil {
		t.Fatalf("PackBundle: %v", err)
	}
	if !st.HasBundle("work") {
		t.Fatal("expected bundle after PackBundle")
	}

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("removing working dir: %v", err)
	}

	if err := st.UnpackBundle(c, "work"); err != nil {
		t.Fatalf("UnpackBundle: %v", err)
	}

	if got := readFile(t, filepath.Join(dir, "Cookies")); got != "cookie-jar" {
		t.Fatalf("Cookies = %q, want cookie-jar", got)
	}
	if got := readFile(t, filepath.Join(dir, "Default", "Preferences")); got != `{"k":"v"}` {
		t.Fatalf("Preferences = %q", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // test reads a path it just wrote
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return string(data)
}
