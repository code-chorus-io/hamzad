package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/infra/config"
)

// Store names used across the table-driven cases below.
const (
	storeWork     = "work"
	storePersonal = "personal"
)

// withConfigRoot points the config root at a temp dir. It cannot run in
// parallel with anything: os.UserConfigDir reads the environment, which is
// process-wide.
func withConfigRoot(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	return filepath.Join(root, "hamzad")
}

// TestResolveNamesSeparateStores is the point of the feature: two names must
// land on two directories, so a work identity set and a personal one never see
// each other's profiles, recipients or session bundles.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestResolveNamesSeparateStores(t *testing.T) {
	root := withConfigRoot(t)

	work := config.Config{Store: storeWork}
	if err := work.Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	personal := config.Config{Store: storePersonal}
	if err := personal.Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if work.StoreDir == personal.StoreDir {
		t.Fatal("two named stores resolved to the same directory")
	}
	if want := filepath.Join(root, "stores", storeWork); work.StoreDir != want {
		t.Errorf("work store = %q, want %q", work.StoreDir, want)
	}
}

// TestResolveDefaultsToTheDefaultStore covers the unnamed case.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestResolveDefaultsToTheDefaultStore(t *testing.T) {
	root := withConfigRoot(t)

	var cfg config.Config
	if err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if want := filepath.Join(root, "stores", config.DefaultStore); cfg.StoreDir != want {
		t.Errorf("StoreDir = %q, want %q", cfg.StoreDir, want)
	}
}

// TestExplicitDirBeatsName keeps the escape hatch working: a store kept outside
// the config root, on an encrypted volume or in a shared checkout, is addressed
// by path and must not be second-guessed by a name.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestExplicitDirBeatsName(t *testing.T) {
	withConfigRoot(t)

	cfg := config.Config{Store: storeWork, StoreDir: "/mnt/secure/store"}
	if err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.StoreDir != "/mnt/secure/store" {
		t.Errorf("StoreDir = %q, want the explicit path untouched", cfg.StoreDir)
	}
}

// TestResolveAdoptsALegacyFlatStore is the safety net. Before named stores, a
// store sat directly in the config root. Resolving past it to stores/default
// would leave those profiles, secrets and session bundles on disk but invisible
// — the tool would look like it had lost them.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestResolveAdoptsALegacyFlatStore(t *testing.T) {
	root := withConfigRoot(t)

	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles.toml"), []byte("[profiles]\n"), 0o600); err != nil {
		t.Fatalf("seeding legacy store: %v", err)
	}

	var cfg config.Config
	if err := cfg.Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if cfg.StoreDir != root {
		t.Errorf("StoreDir = %q, want the existing store at %q", cfg.StoreDir, root)
	}

	// Naming a store still opts into the new layout.
	named := config.Config{Store: storeWork}
	if err := named.Resolve(); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if named.StoreDir == root {
		t.Error("an explicitly named store must not resolve to the legacy root")
	}
}

// TestStorePathRejectsTraversal keeps a name from escaping the config root. The
// name reaches a filesystem path, so "../../etc" would be a directory traversal
// driven by a command-line flag.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestStorePathRejectsTraversal(t *testing.T) {
	withConfigRoot(t)

	for _, bad := range []string{"../escape", "a/b", "", ".", "with space", "/abs"} {
		if _, err := config.StorePath(bad); err == nil {
			t.Errorf("StorePath(%q) was accepted", bad)
		}
	}

	for _, ok := range []string{storeWork, storePersonal, "acme-01", "a_b"} {
		if _, err := config.StorePath(ok); err != nil {
			t.Errorf("StorePath(%q) = %v, want nil", ok, err)
		}
	}
}

// TestListStores reports what exists, and says nothing when nothing does.
//
//nolint:paralleltest // t.Setenv forbids t.Parallel
func TestListStores(t *testing.T) {
	root := withConfigRoot(t)

	got, err := config.ListStores()
	if err != nil {
		t.Fatalf("ListStores on an empty root: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no stores, got %v", got)
	}

	for _, n := range []string{storeWork, storePersonal} {
		if err := os.MkdirAll(filepath.Join(root, "stores", n), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	got, err = config.ListStores()
	if err != nil {
		t.Fatalf("ListStores: %v", err)
	}
	if strings.Join(got, ",") != "personal,work" {
		t.Errorf("ListStores() = %v, want them sorted", got)
	}
}
