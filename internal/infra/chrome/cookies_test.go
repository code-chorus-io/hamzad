package chrome //nolint:testpackage // white-box: exercises the unexported cookie conversion and helper plumbing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCookieParamHostOnlyUsesURL(t *testing.T) {
	t.Parallel()

	p := Cookie{
		Name: "sid", Value: "v", Domain: "accounts.example.com", Path: "/a",
		Secure: true, Expires: 2e9, SameSite: "Lax", PartitionKey: json.RawMessage("null"),
	}.param()

	if _, ok := p["domain"]; ok {
		t.Fatalf("host-only cookie set by domain, which would widen it to subdomains: %v", p)
	}
	if p["url"] != "https://accounts.example.com/a" {
		t.Fatalf("url = %v", p["url"])
	}
	if p["expires"] != 2e9 {
		t.Fatalf("expires = %v", p["expires"])
	}
	if _, ok := p["partitionKey"]; ok {
		t.Fatalf("null partitionKey passed through: %v", p)
	}
}

func TestCookieParamDomainCookie(t *testing.T) {
	t.Parallel()

	p := Cookie{
		Name: "pref", Value: "v", Domain: ".example.com", Path: "/",
		Session: true, Expires: -1, PartitionKey: json.RawMessage(`{"topLevelSite":"https://a.com"}`),
	}.param()

	if p["domain"] != ".example.com" {
		t.Fatalf("domain = %v", p["domain"])
	}
	if _, ok := p["url"]; ok {
		t.Fatalf("domain cookie also got a url: %v", p)
	}
	if _, ok := p["expires"]; ok {
		t.Fatalf("session cookie given an expiry: %v", p)
	}
	if _, ok := p["partitionKey"]; !ok {
		t.Fatalf("partitionKey dropped: %v", p)
	}
}

func TestCookieParamInsecureScheme(t *testing.T) {
	t.Parallel()

	p := Cookie{Name: "a", Domain: "example.org", Path: "/"}.param()
	if p["url"] != "http://example.org/" {
		t.Fatalf("url = %v", p["url"])
	}
}

func TestReadPortFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, devToolsPortFile)

	if got := readPortFile(path); got != "" {
		t.Fatalf("missing file: got %q", got)
	}

	// Half-written: Chrome writes the port line first.
	if err := os.WriteFile(path, []byte("53211\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readPortFile(path); got != "" {
		t.Fatalf("partial file: got %q", got)
	}

	if err := os.WriteFile(path, []byte("53211\n/devtools/browser/abc-123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, want := readPortFile(path), "ws://127.0.0.1:53211/devtools/browser/abc-123"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestRemoveCookieStores(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	keep := filepath.Join(dir, "Default", "Preferences")
	gone := []string{
		filepath.Join(dir, "Default", "Cookies"),
		filepath.Join(dir, "Default", "Cookies-journal"),
		filepath.Join(dir, "Default", "Network", "Cookies"),
		filepath.Join(dir, "Profile 1", "Network", "Cookies-journal"),
	}
	for _, p := range append(gone, keep) {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := removeCookieStores(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range gone {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still present", p)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("Preferences removed: %v", err)
	}
}

func TestImportCookiesRejectsUnknownFormat(t *testing.T) {
	t.Parallel()

	_, _, err := ImportCookies(t.Context(), "/nonexistent", t.TempDir(), []byte(`{"format":99,"cookies":[]}`))
	if err == nil {
		t.Fatal("expected an error for an unknown format")
	}
}
