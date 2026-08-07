package store_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/crypt"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// newSyncStore returns a git-backed store with encryption configured, ready for
// Sync. It skips the test when git is unavailable rather than failing, so the
// suite still runs on a machine without it.
func newSyncStore(t *testing.T) (*store.Store, crypt.Crypt) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	st, c := newCrypt(t)
	if err := st.InitRepo(t.Context(), ""); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}

	// A committer identity must exist for `git commit`, and signing must be off,
	// since the developer's global config may enable it and no key is available
	// here. Both are set on the throwaway repo only.
	for _, kv := range [][2]string{
		{"user.email", "test@example.invalid"},
		{"user.name", "hamzad test"},
		{"commit.gpgsign", "false"},
	} {
		runGit(t, st.Dir, "config", kv[0], kv[1])
	}

	return st, c
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), "git", args...) //nolint:gosec // literal args in a test
	cmd.Dir = dir

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}

	return strings.TrimSpace(string(out))
}

// tracked returns the paths git has under version control, slash-separated.
func tracked(t *testing.T, dir string) []string {
	t.Helper()

	out := runGit(t, dir, "ls-files")
	if out == "" {
		return nil
	}

	return strings.Split(out, "\n")
}

// seedProfile writes a profile plus its encrypted proxy and session bundle —
// the full set of artifacts a teammate needs to resume the account.
func seedProfile(t *testing.T, st *store.Store, c crypt.Crypt, name string) {
	t.Helper()

	if err := st.Add(profile.Profile{Name: name, Timezone: "Europe/Berlin"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := st.SetProxy(c, name, "socks5://user:pass@1.2.3.4:1080"); err != nil {
		t.Fatalf("SetProxy: %v", err)
	}

	dir := st.UserDataDir(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir working dir: %v", err)
	}
	writeFile(t, filepath.Join(dir, "Cookies"), "cookie-jar")

	if err := st.PackBundle(c, name); err != nil {
		t.Fatalf("PackBundle: %v", err)
	}
}

// TestSyncCommitsEverySharedArtifact is the regression guard for a sync that
// staged a fixed file list. It named only profiles.toml and .gitignore, so the
// recipients, the encrypted proxies, and the encrypted session bundles were
// never committed — `store sync` reported success and pushed a repository from
// which no teammate could decrypt anything, which is the entire feature.
func TestSyncCommitsEverySharedArtifact(t *testing.T) {
	t.Parallel()

	st, c := newSyncStore(t)
	seedProfile(t, st, c, "work")
	seedProfile(t, st, c, "personal")

	if err := st.Sync(t.Context(), "share work"); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	files := tracked(t, st.Dir)
	for _, want := range []string{
		"profiles.toml",
		".gitignore",
		"recipients.txt",
		"secrets/work.age",
		"secrets/personal.age",
		"data/work.tar.age",
		"data/personal.tar.age",
	} {
		if !slices.Contains(files, want) {
			t.Errorf("Sync did not commit %q; tracked = %v", want, files)
		}
	}
}

// TestSyncKeepsWorkingDataLocal is the other half of the contract: staging the
// whole store must not sweep up the unencrypted browsing data, which is exactly
// what the README promises stays on the machine.
func TestSyncKeepsWorkingDataLocal(t *testing.T) {
	t.Parallel()

	st, c := newSyncStore(t)
	seedProfile(t, st, c, "work")

	if err := st.Sync(t.Context(), ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	for _, f := range tracked(t, st.Dir) {
		if strings.HasPrefix(f, "data/work/") {
			t.Errorf("Sync committed unencrypted working data: %q", f)
		}
	}
}

// TestSyncIsRepeatable pins the failure that made the command unusable: the
// commit gate tested the worktree (which counts untracked files) while `git
// commit` only ever sees the index, so once everything was committed a second
// sync still tried to commit and git exited 1 with "nothing added to commit".
func TestSyncIsRepeatable(t *testing.T) {
	t.Parallel()

	st, c := newSyncStore(t)
	seedProfile(t, st, c, "work")

	for i := range 3 {
		if err := st.Sync(t.Context(), ""); err != nil {
			t.Fatalf("Sync #%d: %v", i+1, err)
		}
	}

	// Only the first pass had anything to record.
	if got := runGit(t, st.Dir, "rev-list", "--count", "HEAD"); got != "1" {
		t.Errorf("commit count = %s, want 1 — repeat syncs should be no-ops", got)
	}
}

// TestSyncOnEmptyStore covers `store init` immediately followed by `store sync`,
// before any profile exists. Staging a named profiles.toml that had never been
// written aborted with "pathspec did not match any files".
func TestSyncOnEmptyStore(t *testing.T) {
	t.Parallel()

	st, _ := newSyncStore(t)

	if err := st.Sync(t.Context(), ""); err != nil {
		t.Fatalf("Sync on an empty store: %v", err)
	}
}

// TestSyncRecordsRemovals checks a deleted profile actually propagates: without
// staging removals, `profile remove` followed by `store sync` left the secret
// and bundle alive in the shared history.
func TestSyncRecordsRemovals(t *testing.T) {
	t.Parallel()

	st, c := newSyncStore(t)
	seedProfile(t, st, c, "work")

	if err := st.Sync(t.Context(), "add"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := st.Remove("work", true); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if err := st.Sync(t.Context(), "remove"); err != nil {
		t.Fatalf("Sync after Remove: %v", err)
	}

	files := tracked(t, st.Dir)
	for _, gone := range []string{"secrets/work.age", "data/work.tar.age"} {
		if slices.Contains(files, gone) {
			t.Errorf("Sync left %q tracked after the profile was removed", gone)
		}
	}
}

// TestSyncWithoutRepo reports the actionable error rather than an opaque git
// failure.
func TestSyncWithoutRepo(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := st.Ensure(); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	err := st.Sync(context.Background(), "")
	if err == nil {
		t.Fatal("Sync outside a repository must fail")
	}
	if !strings.Contains(err.Error(), "store init") {
		t.Errorf("error should point at 'store init', got %v", err)
	}
}
