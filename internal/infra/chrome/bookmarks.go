package chrome

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Leak-check pages seeded onto a new profile's bookmark bar. They are the
// fastest way to answer "is this profile actually hiding what I think it is" —
// browserleaks reports the address a page really sees, and its WebRTC page
// reports the one WebRTC reveals, which is not always the same.
var seededBookmarks = []struct{ name, url string }{
	{"IP / connection leak check", "https://browserleaks.com/ip"},
	{"WebRTC leak check", "https://browserleaks.com/webrtc"},
}

// chromeEpoch is 1601-01-01 UTC. Chrome stores bookmark timestamps as
// microseconds since then, not since the Unix epoch.
var chromeEpoch = time.Date(1601, time.January, 1, 0, 0, 0, 0, time.UTC)

// seedBookmarks writes a bookmark bar into a profile's user-data directory.
//
// It only ever writes when no Bookmarks file exists yet. Chrome owns that file
// once the profile has been used, and a profile's browsing data is the thing
// this tool is most careful not to damage — so a profile with bookmarks of its
// own is left completely alone. Any error is returned for the caller to treat
// as advisory: failing to add a bookmark is no reason to refuse to launch.
func seedBookmarks(userDataDir string) error {
	if userDataDir == "" {
		return nil
	}

	dir := filepath.Join(userDataDir, "Default")
	path := filepath.Join(dir, "Bookmarks")
	if _, err := os.Stat(path); err == nil {
		return nil // Chrome's file now; never overwrite it
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("creating profile default dir: %w", err)
	}

	raw, err := json.MarshalIndent(bookmarksDocument(time.Now()), "", "   ")
	if err != nil {
		return fmt.Errorf("encoding bookmarks: %w", err)
	}

	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return fmt.Errorf("writing bookmarks: %w", err)
	}

	return nil
}

// bookmarksDocument builds Chrome's Bookmarks JSON.
//
// The "checksum" field is deliberately omitted. Chrome recomputes it and treats
// a missing one as a first read; supplying a wrong one makes it report the file
// as corrupt and discard the contents.
func bookmarksDocument(now time.Time) map[string]any {
	stamp := chromeTimestamp(now)

	children := make([]any, 0, len(seededBookmarks))
	for i, b := range seededBookmarks {
		children = append(children, map[string]any{
			"date_added": stamp,
			// Chrome renumbers on load; these only need to be unique here. The
			// root folders take 1-3, so entries start above them.
			"id":   strconv.Itoa(i + 4),
			"name": b.name,
			"type": "url",
			"url":  b.url,
		})
	}

	folder := func(id, name string, kids []any) map[string]any {
		return map[string]any{
			"children":      kids,
			"date_added":    stamp,
			"date_modified": stamp,
			"id":            id,
			"name":          name,
			"type":          "folder",
		}
	}

	return map[string]any{
		"roots": map[string]any{
			"bookmark_bar": folder("1", "Bookmarks bar", children),
			"other":        folder("2", "Other bookmarks", []any{}),
			"synced":       folder("3", "Mobile bookmarks", []any{}),
		},
		"version": 1,
	}
}

// chromeTimestamp renders t the way Chrome stores it: microseconds since
// 1601-01-01, as a decimal string.
func chromeTimestamp(t time.Time) string {
	return strconv.FormatInt(t.UTC().Sub(chromeEpoch).Microseconds(), 10)
}
