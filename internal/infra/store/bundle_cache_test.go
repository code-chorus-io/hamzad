package store_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// TestPackBundleDropsCacheDirectories is a size guard, not a correctness one.
//
// The bundle is age-encrypted, so it is incompressible and git cannot delta two
// revisions of it: every push writes the whole file again. A real signed-in
// profile packed 197 MB with Chrome's caches included and 66 MB without, against
// GitHub's 50 MB warning and 100 MB hard block. Carrying caches is the
// difference between a store that syncs for years and one that eventually
// refuses pushes — and Chrome rebuilds every one of these on next launch.
func TestPackBundleDropsCacheDirectories(t *testing.T) {
	t.Parallel()

	st, c := newCrypt(t)

	dir := st.UserDataDir("p")
	// Cache locations Chrome actually uses: top level, per-profile, and nested
	// under a profile's Service Worker directory.
	dropped := []string{
		filepath.Join("GrShaderCache", "blob"),
		filepath.Join("GraphiteDawnCache", "blob"),
		filepath.Join("component_crx_cache", "ext.crx"),
		filepath.Join("Default", "Cache", "data_0"),
		filepath.Join("Default", "Code Cache", "js", "index"),
		filepath.Join("Default", "GPUCache", "data_1"),
		filepath.Join("Default", "Service Worker", "CacheStorage", "blob"),
		filepath.Join("Profile 1", "Cache", "data_0"),
	}
	// The identity a shared profile exists to move. None of it may be dropped.
	kept := []string{
		filepath.Join("Default", "Cookies"),
		filepath.Join("Default", "Login Data"),
		filepath.Join("Default", "Preferences"),
		filepath.Join("Default", "Local Storage", "leveldb", "000003.log"),
		filepath.Join("Default", "IndexedDB", "store.db"),
		filepath.Join("Default", "Service Worker", "Database", "000003.log"),
		"Local State",
	}

	for _, rel := range slices.Concat(dropped, kept) {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir %q: %v", rel, err)
		}
		writeFile(t, path, "x")
	}

	if err := st.PackBundle(c, "p"); err != nil {
		t.Fatalf("PackBundle: %v", err)
	}

	// Unpack over a clean directory and see what survived the round trip.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("clearing working dir: %v", err)
	}
	if err := st.UnpackBundle(c, "p"); err != nil {
		t.Fatalf("UnpackBundle: %v", err)
	}

	for _, rel := range kept {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("%q must survive packing, but is missing: %v", rel, err)
		}
	}
	for _, rel := range dropped {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			t.Errorf("%q is a rebuildable cache and must not be packed", rel)
		}
	}
}

// TestPackBundleKeepsExtensionCacheDirectories stops the exclusion from being
// over-eager. Matching "Cache" at any depth would also drop a directory an
// extension happens to name that way inside its own storage, which is real
// profile data — so exactly one leading profile component is stripped, no more.
func TestPackBundleKeepsExtensionCacheDirectories(t *testing.T) {
	t.Parallel()

	st, c := newCrypt(t)

	dir := st.UserDataDir("p")
	nested := filepath.Join("Default", "Local Extension Settings", "abcdef", "Cache", "value")
	path := filepath.Join(dir, nested)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, path, "extension state")

	if err := st.PackBundle(c, "p"); err != nil {
		t.Fatalf("PackBundle: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("clearing working dir: %v", err)
	}
	if err := st.UnpackBundle(c, "p"); err != nil {
		t.Fatalf("UnpackBundle: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("an extension's own Cache directory is profile data and must be packed: %v", err)
	}
}

// newBareRemote returns an empty bare repository standing in for a
// freshly-created GitHub repo, plus a function that points its HEAD at whatever
// branch the store actually pushed.
//
// The branch name is never assumed: `git init` defaults to "main" on a
// developer's machine and "master" on older git, so hard-coding either makes the
// test pass in one place and fail in the other.
func newBareRemote(t *testing.T, st *store.Store) (string, func() string) {
	t.Helper()

	remote := t.TempDir()
	runGit(t, remote, "init", "--bare", ".")
	runGit(t, st.Dir, "remote", "add", "origin", remote)

	adopt := func() string {
		branch := runGit(t, st.Dir, "rev-parse", "--abbrev-ref", "HEAD")
		runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/"+branch)

		return branch
	}

	return remote, adopt
}

// TestSyncBootstrapsAnEmptyRemote covers the documented first-run sequence:
// `store init --remote <fresh repo>` then `store sync`. Sync rebased before
// pushing, and rebasing onto a repository with no commits fails outright with
// "couldn't find remote ref HEAD" — so following the README against a
// newly-created repo broke on step two, every time.
func TestSyncBootstrapsAnEmptyRemote(t *testing.T) {
	t.Parallel()

	st, c := newSyncStore(t)
	seedProfile(t, st, c, "work")

	// A bare repository with no commits is exactly what `gh repo create` leaves
	// behind, and what the old pull --rebase could not cope with.
	remote, adopt := newBareRemote(t, st)

	if err := st.Sync(t.Context(), "first sync"); err != nil {
		t.Fatalf("Sync onto an empty remote: %v", err)
	}

	// The bundle and the profile must actually be on the remote, not merely
	// committed locally with the push silently skipped.
	out := runGit(t, remote, "ls-tree", "-r", "--name-only", adopt())
	for _, want := range []string{"profiles.toml", "recipients.txt", workBundlePath} {
		if !slices.Contains(splitLines(out), want) {
			t.Errorf("%q missing from the remote after the first sync; got %v", want, splitLines(out))
		}
	}
}

// TestSyncStillRebasesOnceTheRemoteHasCommits keeps the bootstrap shortcut from
// turning into "never pull": a second sync must still integrate remote work
// before pushing, or concurrent teammates would overwrite each other.
func TestSyncStillRebasesOnceTheRemoteHasCommits(t *testing.T) {
	t.Parallel()

	st, c := newSyncStore(t)
	seedProfile(t, st, c, "work")

	remote, adopt := newBareRemote(t, st)

	if err := st.Sync(t.Context(), "first"); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	branch := adopt()

	// A teammate's commit lands on the remote, then we sync again. If the pull
	// were skipped the push would be rejected as non-fast-forward.
	other := t.TempDir()
	runGit(t, other, "clone", remote, ".")
	for _, kv := range [][2]string{
		{"user.email", "other@example.invalid"},
		{"user.name", "other"},
		{"commit.gpgsign", "false"},
	} {
		runGit(t, other, "config", kv[0], kv[1])
	}
	writeFile(t, filepath.Join(other, "notes.txt"), "from a teammate")
	runGit(t, other, "add", "-A", ".")
	runGit(t, other, "commit", "-m", "teammate change")
	runGit(t, other, "push", "origin", "HEAD")

	seedProfile(t, st, c, "second")
	if err := st.Sync(t.Context(), "second"); err != nil {
		t.Fatalf("second Sync must rebase onto the teammate's commit: %v", err)
	}

	out := runGit(t, remote, "ls-tree", "-r", "--name-only", branch)
	for _, want := range []string{"notes.txt", "data/second.tar.age"} {
		if !slices.Contains(splitLines(out), want) {
			t.Errorf("%q missing after the second sync; got %v", want, splitLines(out))
		}
	}
}

// splitLines is a nil-safe strings.Split for git's newline-separated output.
func splitLines(out string) []string {
	if out == "" {
		return nil
	}

	return strings.Split(out, "\n")
}
