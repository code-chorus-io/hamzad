package browser

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ErrBrowserUnusable is returned when an installed browser is present but
// cannot start — almost always missing system libraries on a minimal Linux
// install, which Chrome reports as a bare loader error at launch.
var ErrBrowserUnusable = errors.New("installed browser cannot start")

// verifyTimeout bounds the `--version` probe. Printing a version string is
// nearly instant; anything slower is a hang worth reporting as a failure.
const verifyTimeout = 20 * time.Second

// Verify runs the installed binary to confirm it actually starts. Downloading
// Chrome is not enough on Linux: the archive carries no system libraries, so a
// container or minimal desktop is missing several of them and every launch dies
// with "error while loading shared libraries". Catching that at install time
// turns a confusing failure at `profile open` into an actionable one here.
func Verify(ctx context.Context, execPath string) error {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, execPath, "--version")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", ErrBrowserUnusable, describeFailure(ctx, execPath, stderr.String()))
	}

	return nil
}

// describeFailure turns the loader's complaint into something a user can act
// on, naming every missing library rather than just the first one Chrome hit.
func describeFailure(ctx context.Context, execPath, stderr string) string {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = "the binary exited without starting"
	}

	missing := missingLibraries(ctx, execPath)
	if len(missing) == 0 {
		return msg
	}

	return fmt.Sprintf("%s\n  missing system libraries: %s\n  install them with your package manager "+
		"(Debian/Ubuntu: libatk1.0-0t64 libatk-bridge2.0-0t64 libatspi2.0-0t64 libxcomposite1 libxdamage1 "+
		"libnss3 libgbm1 libasound2t64; Arch: at-spi2-core nss libxcomposite libxdamage mesa alsa-lib)",
		msg, strings.Join(missing, ", "))
}

// missingLibraries asks ldd which shared objects cannot be resolved. It is
// best-effort: ldd is Linux-only and may be absent, in which case the caller
// still gets the loader's own message.
func missingLibraries(ctx context.Context, execPath string) []string {
	if runtime.GOOS != "linux" {
		return nil
	}
	if _, err := exec.LookPath("ldd"); err != nil {
		return nil
	}

	//nolint:gosec // execPath is a path we installed into our own cache, not user input
	out, err := exec.CommandContext(ctx, "ldd", execPath).Output()
	if err != nil && len(out) == 0 {
		return nil
	}

	seen := map[string]struct{}{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if !strings.Contains(line, "not found") {
			continue
		}
		if name := strings.TrimSpace(strings.Fields(line)[0]); name != "" {
			seen[name] = struct{}{}
		}
	}

	names := make([]string, 0, len(seen))
	for n := range seen {
		names = append(names, n)
	}
	sort.Strings(names)

	return names
}
