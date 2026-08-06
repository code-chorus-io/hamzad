// Package browser installs and tracks the Chrome build a profile launches.
//
// It fetches Chrome for Testing: the same browser Google ships to users, built
// for automation and — crucially — pinned, so it never auto-updates underneath a
// profile. That matters more here than it would elsewhere. An anti-detect
// profile is only convincing while its advertised user-agent matches the engine
// actually rendering the page, and a teammate resuming a shared profile on a
// different Chrome is running a different identity than the one that was shared.
//
// Chromium is deliberately not an option. Its openness buys nothing without a
// recompile, and a stock Chromium binary is *more* identifiable than Chrome: it
// lacks the proprietary codecs (H.264/AAC), reports "Chromium" rather than
// "Google Chrome" in its user-agent-client-hint brands, and ships no Widevine —
// three signals a fingerprinter reads for free.
package browser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
)

// ErrPlatformUnsupported is returned when Chrome for Testing publishes no build
// for the host platform.
var ErrPlatformUnsupported = errors.New("chrome for testing publishes no build for this platform")

// ErrNotInstalled is returned when a requested version is absent from the cache.
var ErrNotInstalled = errors.New("browser version is not installed")

// metadataFile records what was installed, beside the install itself.
const metadataFile = "metadata.json"

// The platform keys Chrome for Testing publishes under. This is the complete
// set — notably there is no linux-arm64, which is why Platform can fail.
const (
	platformLinux64  = "linux64"
	platformMacX64   = "mac-x64"
	platformMacARM64 = "mac-arm64"
	platformWin32    = "win32"
	platformWin64    = "win64"
)

// Metadata describes an installed browser. It is written at install time and
// read back on every launch, so a corrupted or half-extracted install is
// distinguishable from a complete one.
type Metadata struct {
	Version   string `json:"version"`
	Platform  string `json:"platform"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Installed string `json:"installed"`
}

// Platform returns the Chrome for Testing platform key for the host.
//
// linux/arm64 has no published build — Google ships linux64 only — so it fails
// here rather than at download time, with an error that says what to do instead.
func Platform() (string, error) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return platformLinux64, nil
	case "darwin/amd64":
		return platformMacX64, nil
	case "darwin/arm64":
		return platformMacARM64, nil
	case "windows/amd64":
		return platformWin64, nil
	case "windows/386":
		return platformWin32, nil
	default:
		return "", fmt.Errorf("%w: %s/%s (install a system Chrome/Chromium and use --chrome-path)",
			ErrPlatformUnsupported, runtime.GOOS, runtime.GOARCH)
	}
}

// execRelPath is where the browser binary sits inside an extracted archive.
// The layout is fixed per platform: a single top-level directory named for the
// platform, holding the binary directly on Linux and Windows and a .app bundle
// on macOS.
func execRelPath(platform string) string {
	switch platform {
	case platformLinux64:
		return filepath.Join("chrome-"+platform, "chrome")
	case platformMacX64, platformMacARM64:
		return filepath.Join("chrome-"+platform,
			"Google Chrome for Testing.app", "Contents", "MacOS", "Google Chrome for Testing")
	case platformWin32, platformWin64:
		return filepath.Join("chrome-"+platform, "chrome.exe")
	default:
		return ""
	}
}

// CacheDir is where installed browsers live. They are large and re-downloadable,
// so they belong in the cache directory rather than beside the profile store.
func CacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolving cache dir: %w", err)
	}

	return filepath.Join(base, "koochooloologin", "browsers"), nil
}

// versionDir is the install root for one version.
func versionDir(version string) (string, error) {
	base, err := CacheDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(base, version), nil
}

// ExecPath returns the browser binary for an installed version, or
// ErrNotInstalled. It verifies the recorded metadata and the binary are both
// present, so a partial install reports as missing instead of failing later
// inside the launcher.
func ExecPath(version string) (string, error) {
	dir, err := versionDir(version)
	if err != nil {
		return "", err
	}

	meta, err := readMetadata(dir)
	if err != nil {
		return "", err
	}

	bin := filepath.Join(dir, execRelPath(meta.Platform))
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("%w: %q is incomplete, reinstall it", ErrNotInstalled, version)
	}

	return bin, nil
}

// List returns the installed versions, newest-looking last, with their metadata.
func List() ([]Metadata, error) {
	base, err := CacheDir()
	if err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(base)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading browser cache: %w", err)
	}

	var out []Metadata
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := readMetadata(filepath.Join(base, e.Name()))
		if err != nil {
			continue // a half-removed or foreign directory is not an install
		}
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool { return lessVersion(out[i].Version, out[j].Version) })

	return out, nil
}

// Newest returns the highest installed version, or ErrNotInstalled when the
// cache holds none.
func Newest() (Metadata, error) {
	all, err := List()
	if err != nil {
		return Metadata{}, err
	}
	if len(all) == 0 {
		return Metadata{}, ErrNotInstalled
	}

	return all[len(all)-1], nil
}

// Remove deletes an installed version.
func Remove(version string) error {
	dir, err := versionDir(version)
	if err != nil {
		return err
	}
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %q", ErrNotInstalled, version)
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing %q: %w", version, err)
	}

	return nil
}
