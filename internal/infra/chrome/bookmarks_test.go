package chrome //nolint:testpackage // white-box: exercises the unexported bookmark seeding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestSeedBookmarksWritesLeakChecks covers the bookmark bar a fresh profile
// opens with.
func TestSeedBookmarksWritesLeakChecks(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := seedBookmarks(dir); err != nil {
		t.Fatalf("seedBookmarks: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "Default", "Bookmarks")) //nolint:gosec // test reads what it wrote
	if err != nil {
		t.Fatalf("reading bookmarks: %v", err)
	}

	var doc struct {
		Roots struct {
			BookmarkBar struct {
				Children []struct {
					Name string `json:"name"`
					URL  string `json:"url"`
					Type string `json:"type"`
				} `json:"children"`
			} `json:"bookmark_bar"`
		} `json:"roots"`
		Checksum string `json:"checksum"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("bookmarks file is not valid JSON: %v", err)
	}

	// A wrong checksum makes Chrome declare the file corrupt and drop it.
	if doc.Checksum != "" {
		t.Error("must not write a checksum; Chrome recomputes it")
	}

	urls := make([]string, 0, len(doc.Roots.BookmarkBar.Children))
	for _, c := range doc.Roots.BookmarkBar.Children {
		urls = append(urls, c.URL)
	}
	if !slices.Contains(urls, "https://browserleaks.com/ip") {
		t.Errorf("bookmark bar missing the IP leak check; got %v", urls)
	}
}

// TestSeedBookmarksNeverOverwrites is the safety property: once Chrome owns the
// file it holds the user's real bookmarks, and clobbering a teammate's shared
// session data would be the worst kind of bug this tool could have.
func TestSeedBookmarksNeverOverwrites(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	defaultDir := filepath.Join(dir, "Default")
	if err := os.MkdirAll(defaultDir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const existing = `{"roots":{"bookmark_bar":{"children":[{"name":"mine","url":"https://example.com"}]}}}`
	path := filepath.Join(defaultDir, "Bookmarks")
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("seeding existing bookmarks: %v", err)
	}

	if err := seedBookmarks(dir); err != nil {
		t.Fatalf("seedBookmarks: %v", err)
	}

	got, err := os.ReadFile(path) //nolint:gosec // test reads what it wrote
	if err != nil {
		t.Fatalf("re-reading: %v", err)
	}
	if strings.TrimSpace(string(got)) != existing {
		t.Error("seedBookmarks overwrote bookmarks Chrome already owned")
	}
}
