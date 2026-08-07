package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// newIdentity generates an age identity and writes it to a file, returning the
// key file path and the public recipient line.
func newIdentity(t *testing.T) (string, string) {
	t.Helper()

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("GenerateX25519Identity: %v", err)
	}

	path := filepath.Join(t.TempDir(), "identity.txt")
	if err := os.WriteFile(path, []byte(id.String()+"\n"), 0o600); err != nil {
		t.Fatalf("write identity: %v", err)
	}

	return path, id.Recipient().String()
}

// writeRecipients replaces the store's recipients file with the given lines.
func writeRecipients(t *testing.T, st *store.Store, keys ...string) {
	t.Helper()

	body := strings.Join(keys, "\n") + "\n"
	if err := os.WriteFile(st.RecipientsPath(), []byte(body), 0o600); err != nil {
		t.Fatalf("write recipients: %v", err)
	}
}

// seedEncrypted stores an encrypted proxy and a session bundle for name.
func seedEncrypted(t *testing.T, st *store.Store, c crypt.Crypt, name, proxy string) {
	t.Helper()

	if err := st.SetProxy(c, name, proxy); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}

	dir := st.UserDataDir(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "Cookies"), "cookie-jar")

	if err := st.PackBundle(c, name); err != nil {
		t.Fatalf("PackBundle: %v", err)
	}
}

// TestReencryptAdmitsANewRecipient is the case the command exists for: a key
// added after the fact cannot read anything until the store is re-encrypted.
func TestReencryptAdmitsANewRecipient(t *testing.T) {
	t.Parallel()

	st := newStore(t)

	oldID, oldKey := newIdentity(t)
	writeRecipients(t, st, oldKey)

	old := crypt.New(st.RecipientsPath(), oldID)
	const proxy = "socks5://user:pass@1.2.3.4:1080"
	seedEncrypted(t, st, old, "work", proxy)

	// The teammate joins after the secret and bundle were written.
	newID, newKey := newIdentity(t)
	writeRecipients(t, st, oldKey, newKey)
	teammate := crypt.New(st.RecipientsPath(), newID)

	if _, err := st.Proxy(teammate, "work"); err == nil {
		t.Fatal("a recipient added after the write could already decrypt; nothing to fix")
	}

	results, err := st.Reencrypt(old)
	if err != nil {
		t.Fatalf("Reencrypt: %v", err)
	}
	assertAllReencrypted(t, results, 2)

	assertProxy(t, st, teammate, "work", proxy)
	assertBundleIntact(t, st, teammate, "work")

	// The original recipient must not be locked out by the rewrite.
	assertProxy(t, st, old, "work", proxy)
}

// assertAllReencrypted fails unless results holds want entries, all successful.
func assertAllReencrypted(t *testing.T, results []store.Reencrypted, want int) {
	t.Helper()

	if len(results) != want {
		t.Fatalf("got %d results, want %d", len(results), want)
	}
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s %q: %v", r.Kind, r.Name, r.Err)
		}
	}
}

// assertProxy fails unless the profile's secret decrypts to want with c.
func assertProxy(t *testing.T, st *store.Store, c crypt.Crypt, name, want string) {
	t.Helper()

	got, err := st.Proxy(c, name)
	if err != nil {
		t.Fatalf("Proxy %q: %v", name, err)
	}
	if got != want {
		t.Fatalf("Proxy %q = %q, want %q", name, got, want)
	}
}

// assertBundleIntact fails unless the bundle decrypts with c and still unpacks
// to the content seedEncrypted put in it.
func assertBundleIntact(t *testing.T, st *store.Store, c crypt.Crypt, name string) {
	t.Helper()

	if err := os.RemoveAll(st.UserDataDir(name)); err != nil {
		t.Fatalf("removing working dir: %v", err)
	}
	if err := st.UnpackBundle(c, name); err != nil {
		t.Fatalf("UnpackBundle %q: %v", name, err)
	}
	if got := readFile(t, filepath.Join(st.UserDataDir(name), "Cookies")); got != "cookie-jar" {
		t.Fatalf("Cookies = %q, want cookie-jar", got)
	}
}

// newStore returns an initialized store in a temp directory.
func newStore(t *testing.T) *store.Store {
	t.Helper()

	st := store.New(t.TempDir())
	if err := st.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	return st
}

// TestReencryptReportsUndecryptable checks that an artifact the running
// identity cannot read is reported rather than silently skipped or destroyed.
func TestReencryptReportsUndecryptable(t *testing.T) {
	t.Parallel()

	st := newStore(t)

	ownerID, ownerKey := newIdentity(t)
	writeRecipients(t, st, ownerKey)
	seedEncrypted(t, st, crypt.New(st.RecipientsPath(), ownerID), "work", "socks5://1.2.3.4:1080")

	// A stranger holds a recipient slot but not the identity the store was
	// written to, so both artifacts are unreadable to them.
	strangerID, strangerKey := newIdentity(t)
	writeRecipients(t, st, ownerKey, strangerKey)
	stranger := crypt.New(st.RecipientsPath(), strangerID)

	before := readBytes(t, filepath.Join(st.Dir, "secrets", "work.age"))

	results, err := st.Reencrypt(stranger)
	if err != nil {
		t.Fatalf("Reencrypt: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	for _, r := range results {
		if r.Err == nil {
			t.Fatalf("%s %q re-encrypted with the wrong identity", r.Kind, r.Name)
		}
	}

	if after := readBytes(t, filepath.Join(st.Dir, "secrets", "work.age")); string(after) != string(before) {
		t.Fatal("a secret that could not be decrypted was overwritten")
	}
	// Leftover temp files would be committed by the next sync.
	assertNoTempFiles(t, filepath.Join(st.Dir, "secrets"))
	assertNoTempFiles(t, filepath.Join(st.Dir, "data"))
}

// TestReencryptEmptyStore covers a store with recipients but nothing encrypted.
func TestReencryptEmptyStore(t *testing.T) {
	t.Parallel()

	st := newStore(t)

	id, key := newIdentity(t)
	writeRecipients(t, st, key)

	results, err := st.Reencrypt(crypt.New(st.RecipientsPath(), id))
	if err != nil {
		t.Fatalf("Reencrypt: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("got %d results, want 0", len(results))
	}
}

// TestReencryptWithoutRecipients refuses rather than writing files nobody can
// read.
func TestReencryptWithoutRecipients(t *testing.T) {
	t.Parallel()

	st := newStore(t)

	id, _ := newIdentity(t)

	_, err := st.Reencrypt(crypt.New(st.RecipientsPath(), id))
	if !errors.Is(err, crypt.ErrNoRecipients) {
		t.Fatalf("Reencrypt error = %v, want ErrNoRecipients", err)
	}
}

// TestReencryptSkipsWorkingCopies makes sure the unencrypted data/<name>/
// directory beside a bundle is not mistaken for an artifact.
func TestReencryptSkipsWorkingCopies(t *testing.T) {
	t.Parallel()

	st := newStore(t)

	id, key := newIdentity(t)
	writeRecipients(t, st, key)
	c := crypt.New(st.RecipientsPath(), id)
	seedEncrypted(t, st, c, "work", "socks5://1.2.3.4:1080")

	// The working copy from seedEncrypted is still on disk, and a stale temp file
	// sits next to the bundle.
	writeFile(t, filepath.Join(st.Dir, "data", ".work-123.tmp"), "half written")

	results, err := st.Reencrypt(c)
	if err != nil {
		t.Fatalf("Reencrypt: %v", err)
	}

	var bundles int
	for _, r := range results {
		if r.Kind == store.BundleKind {
			bundles++
			if r.Name != "work" {
				t.Fatalf("bundle name = %q, want work", r.Name)
			}
		}
		if r.Err != nil {
			t.Fatalf("%s %q: %v", r.Kind, r.Name, r.Err)
		}
	}
	if bundles != 1 {
		t.Fatalf("got %d bundles, want 1", bundles)
	}
}

func readBytes(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // test reads a path the store just wrote
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	return data
}

// assertNoTempFiles fails when dir holds a leftover .tmp file.
func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover temp file %s in %s", e.Name(), dir)
		}
	}
}
