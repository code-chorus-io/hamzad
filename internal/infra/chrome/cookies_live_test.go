//go:build live

package chrome //nolint:testpackage // white-box: builds cookie documents from the unexported types

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// TestCookiesRoundTripLive moves a set of cookies through a real browser the way
// a shared session does: import into a profile whose cookie store is gone (the
// state a bundle from another machine leaves once its undecryptable Cookies file
// is dropped), quit, start again, export, and compare.
func TestCookiesRoundTripLive(t *testing.T) {
	execPath, err := Resolve("", "")
	if err != nil {
		t.Skipf("no browser: %v", err)
	}

	dir := filepath.Join(t.TempDir(), "profile")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}

	expires := float64(time.Now().Add(30 * 24 * time.Hour).Unix())
	in := []exportedCookie{
		{Name: "sid", Value: "host-only-secret", Domain: "accounts.example.com", Path: "/",
			Expires: expires, Secure: true, HTTPOnly: true, SameSite: "Lax", SourceScheme: "Secure", SourcePort: 443},
		{Name: "pref", Value: "domain-wide", Domain: ".example.com", Path: "/",
			Expires: expires, Secure: true, SameSite: "None", SourceScheme: "Secure", SourcePort: 443},
		{Name: "plain", Value: "http-only-site", Domain: "insecure.example.org", Path: "/app",
			Expires: expires, SourceScheme: "NonSecure", SourcePort: 80},
	}
	doc := cookiesDoc{Format: cookiesFormat}
	for _, c := range in {
		raw, _ := json.Marshal(c)
		doc.Cookies = append(doc.Cookies, raw)
	}
	data, _ := json.Marshal(doc)

	ctx := context.Background()

	written, refused, err := ImportCookies(ctx, execPath, dir, data)
	if err != nil {
		t.Fatalf("ImportCookies: %v", err)
	}
	if written != len(in) || refused != 0 {
		t.Fatalf("ImportCookies wrote %d, refused %d; want %d, 0", written, refused, len(in))
	}

	// Chrome has kept the database in both places over the years.
	db := filepath.Join(dir, "Default", "Network", "Cookies")
	if _, err := os.Stat(db); err != nil {
		db = filepath.Join(dir, "Default", "Cookies")
	}
	if _, err := os.Stat(db); err != nil {
		t.Fatalf("no cookie store on disk after import: %v", err)
	}
	// The values must be stored encrypted under this machine's key, as Chrome
	// itself would have written them, not as plaintext.
	if sqlite, err := exec.LookPath("sqlite3"); err == nil {
		out, err := exec.Command(sqlite, db,
			"select count(*) from cookies where value = '' and hex(substr(encrypted_value,1,3)) in ('763130','763131')").Output()
		if err != nil {
			t.Fatalf("reading %s: %v", db, err)
		}
		// "v10"/"v11": the OS-keyed ciphertext prefixes.
		if string(out) != "3\n" {
			t.Errorf("want 3 encrypted rows, got %q", out)
		}
	}

	out, n, err := ExportCookies(ctx, execPath, dir)
	if err != nil {
		t.Fatalf("ExportCookies: %v", err)
	}
	if n != len(in) {
		t.Fatalf("exported %d cookies, want %d: %s", n, len(in), out)
	}

	var got cookiesDoc
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	type key struct{ name, domain, path, value string }
	var want, have []key
	for _, c := range in {
		want = append(want, key{c.Name, c.Domain, c.Path, c.Value})
	}
	for _, raw := range got.Cookies {
		var c exportedCookie
		_ = json.Unmarshal(raw, &c)
		have = append(have, key{c.Name, c.Domain, c.Path, c.Value})
	}
	less := func(s []key) func(i, j int) bool {
		return func(i, j int) bool { return s[i].name < s[j].name }
	}
	sort.Slice(want, less(want))
	sort.Slice(have, less(have))
	for i := range want {
		if want[i] != have[i] {
			t.Errorf("cookie %d: got %+v, want %+v", i, have[i], want[i])
		}
	}

	if _, err := os.Stat(filepath.Join(dir, devToolsPortFile)); err == nil {
		t.Error("DevToolsActivePort left behind in the profile")
	}
	t.Logf("browser: %s", got.Browser)
}
