// Package config loads the application configuration, layered from hardcoded
// defaults, an optional on-disk TOML file, and environment variables. It also
// carries the resolved configuration through the command tree via context.
package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/toml/v2"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
)

// EnvPrefix is the prefix for environment-variable overrides, e.g.
// KEL_STORE_DIR overrides store_dir. Nested keys use a double underscore.
const EnvPrefix = "KEL_"

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
	// StoreDir is the directory holding the profile store (a git repository).
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
		StoreDir:      defaultStoreDir(),
		IdentityPath:  defaultIdentityPath(),
	}
}

func defaultIdentityPath() string {
	return filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519")
}

func defaultStoreDir() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "koochooloologin")
	}

	return filepath.Join(os.Getenv("HOME"), ".koochooloologin")
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
