package chrome //nolint:testpackage // white-box: exercises the unexported clearSingletonLocks

import (
	"os"
	"path/filepath"
	"testing"
)

// TestClearSingletonLocksRemovesOrphans reproduces the exact on-disk state a
// SIGKILL-ed Chrome leaves behind — the three ProcessSingleton symlinks — and
// asserts clearSingletonLocks removes them so the next launch does not hand off
// to a dead browser ("Opening in existing browser session.").
func TestClearSingletonLocksRemovesOrphans(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Chrome writes these as symlinks (SingletonLock -> host-pid, etc.). The
	// targets need not exist; only the links matter to ProcessSingleton.
	links := map[string]string{
		"SingletonLock":   "millennium-falcon-9959",
		"SingletonCookie": "12913063572135028314",
		"SingletonSocket": "/tmp/does-not-exist/SingletonSocket",
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatalf("seeding %q: %v", name, err)
		}
	}

	// A real profile file must survive the cleanup untouched.
	keep := filepath.Join(dir, "Preferences")
	if err := os.WriteFile(keep, []byte("{}"), 0o600); err != nil {
		t.Fatalf("seeding Preferences: %v", err)
	}

	clearSingletonLocks(dir)

	for name := range links {
		if _, err := os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("expected %q removed, Lstat err = %v", name, err)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("clearSingletonLocks must not touch profile data: %v", err)
	}
}

// TestClearSingletonLocksEmptyDir is a no-op for an empty dir (chromedp then
// allocates its own temp dir) and must not panic.
func TestClearSingletonLocksEmptyDir(t *testing.T) {
	t.Parallel()

	clearSingletonLocks("")
}
