package chrome

import (
	"errors"
	"fmt"

	"github.com/code-chorus-io/hamzad/internal/infra/browser"
)

// ErrVersionNotInstalled is returned when a pinned browser version has not been
// downloaded yet. It names the command that fixes it, because the alternative —
// pulling ~190 MB in the middle of `profile open` — is not something a launch
// should decide to do on its own.
var ErrVersionNotInstalled = errors.New("pinned browser version is not installed")

// Resolve picks the browser binary to launch, in order:
//
//  1. an explicitly configured path (--chrome-path / chrome_path)
//  2. the pinned managed version (chrome_version), which must be installed
//  3. a system Chrome/Chromium found on PATH
//  4. the newest managed install, so `browser install` alone is enough on a
//     machine with no system Chrome
//
// Pinning deliberately outranks the system browser: a profile that names a
// version is asserting which engine its fingerprint was built around, and
// silently launching a different one is how a shared profile stops matching the
// identity it was shared as.
func Resolve(explicitPath, pinnedVersion string) (string, error) {
	if explicitPath != "" {
		return explicitPath, nil
	}

	if pinnedVersion != "" {
		path, err := browser.ExecPath(pinnedVersion)
		if err != nil {
			return "", fmt.Errorf("%w: %s — run 'browser install %s'",
				ErrVersionNotInstalled, pinnedVersion, pinnedVersion)
		}

		return path, nil
	}

	if path := Detect(); path != "" {
		return path, nil
	}

	if meta, err := browser.Newest(); err == nil {
		path, err := browser.ExecPath(meta.Version)
		if err == nil {
			return path, nil
		}
	}

	return "", ErrNoBrowser
}
