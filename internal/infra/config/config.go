// Package config loads the application configuration, layered from hardcoded
// defaults, an optional on-disk TOML file, and environment variables. It also
// carries the resolved configuration through the command tree via context.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

// EnvPrefix is the prefix for environment-variable overrides, e.g.
// HAMZAD_STORE_DIR overrides store_dir. Nested keys use a double underscore.
const EnvPrefix = "HAMZAD_"

// Config holds the resolved application configuration.
type Config struct {
	// ChromePath is the path to the Chrome/Chromium executable. When empty,
	// the launcher auto-detects a browser from well-known locations.
	ChromePath string `json:"chrome_path" koanf:"chrome_path"`
	// ChromeVersion pins a managed Chrome for Testing build (e.g. "151.0.7922.76")
	// installed with `browser install`. It outranks a detected system browser, so
	// a profile's fingerprint stays matched to the engine it was built around.
	// Empty means "use whatever the host has".
	ChromeVersion string `json:"chrome_version" koanf:"chrome_version"`
	// Store names which store to use — "work", "personal". Stores are separate
	// git repositories under the config root, each with its own profiles,
	// recipients and session bundles, so a work identity set can be shared with
	// colleagues while a personal one stays private. Empty means DefaultStore.
	Store string `json:"store" koanf:"store"`
	// StoreDir is the resolved directory holding the store. Setting it directly
	// is the escape hatch for a store kept outside the config root — a shared
	// checkout, an encrypted volume — and it overrides Store when both are given.
	StoreDir string `json:"store_dir" koanf:"store_dir"`
	// IdentityPath is the age or SSH private key used to decrypt secrets and
	// session bundles. Encryption never needs it — only decryption does.
	IdentityPath string `json:"identity_path" koanf:"identity_path"`
}

// Default returns the hardcoded configuration defaults.
func Default() Config {
	return Config{
		ChromePath:    "",
		ChromeVersion: "",
		Store:         "",
		// Left empty so Resolve can tell "not set" from "set to the default", and
		// pick the path from Store instead.
		StoreDir:     "",
		IdentityPath: defaultIdentityPath(),
	}
}

func defaultIdentityPath() string {
	return filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519")
}

// DefaultStore is the store used when none is named.
const DefaultStore = "default"

// storesDir holds the named stores, one directory each.
const storesDir = "stores"

// ErrInvalidStoreName is returned for a name that cannot be a directory.
var ErrInvalidStoreName = errors.New("invalid store name")

// storeNameRE keeps a name usable as a single path component, so it cannot
// escape the config root or collide with the layout around it.
var storeNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// Root is the directory holding configuration and the named stores.
func Root() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "hamzad")
	}

	return filepath.Join(os.Getenv("HOME"), ".hamzad")
}

// StorePath is where a named store lives.
func StorePath(name string) (string, error) {
	if !storeNameRE.MatchString(name) {
		return "", fmt.Errorf("%w: %q (use letters, digits, '-' or '_')", ErrInvalidStoreName, name)
	}

	return filepath.Join(Root(), storesDir, name), nil
}

// Resolve turns a store name into a directory, once every layer of
// configuration has had its say. An explicit StoreDir always wins.
//
// A store laid out directly in the config root — the single-store layout that
// predates named stores — keeps working untouched: adopting it in place is the
// only way to avoid silently orphaning profiles that are already there.
func (c *Config) Resolve() error {
	if c.StoreDir != "" {
		return nil
	}

	if c.Store == "" {
		if legacyRootStore() {
			c.StoreDir = Root()

			return nil
		}
		c.Store = DefaultStore
	}

	dir, err := StorePath(c.Store)
	if err != nil {
		return err
	}
	c.StoreDir = dir

	return nil
}

// legacyRootStore reports whether the config root is itself a store, which is
// how a single-store installation looks.
func legacyRootStore() bool {
	_, err := os.Stat(filepath.Join(Root(), "profiles.toml"))

	return err == nil
}

// ListStores returns the named stores that exist, sorted.
func ListStores() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(Root(), storesDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading stores: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	return names, nil
}

// Load builds the configuration from defaults, then the file at path (when it
// exists and path is non-empty), then environment variables. Later layers win.
func Load(path string) (Config, error) {
	k := koanf.New(".")

	if err := k.Load(structs.Provider(Default(), "koanf"), nil); err != nil {
		return Config{}, fmt.Errorf("loading defaults: %w", err)
	}

	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if err := k.Load(file.Provider(path), toml.Parser()); err != nil {
				return Config{}, fmt.Errorf("loading config file %q: %w", path, err)
			}
		}
	}

	transform := func(key, val string) (string, any) {
		key = strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(key, EnvPrefix)), "__", ".")

		return key, val
	}
	if err := k.Load(env.Provider(".", env.Opt{Prefix: EnvPrefix, TransformFunc: transform}), nil); err != nil {
		return Config{}, fmt.Errorf("loading environment: %w", err)
	}

	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return Config{}, fmt.Errorf("unmarshaling config: %w", err)
	}

	return cfg, nil
}

type ctxKey struct{}

// With returns a copy of ctx carrying cfg.
func With(ctx context.Context, cfg *Config) context.Context {
	return context.WithValue(ctx, ctxKey{}, cfg)
}

// From retrieves the configuration carried by ctx, or nil when absent.
func From(ctx context.Context) *Config {
	cfg, _ := ctx.Value(ctxKey{}).(*Config)

	return cfg
}
