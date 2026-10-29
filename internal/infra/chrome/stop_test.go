//go:build !windows

package chrome //nolint:testpackage // white-box: drives the unexported clean launch

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCancelAsksChromeToQuit pins the fix for sessions saved half-written: a
// cancelled session (Ctrl-C, or the TUI closing it) must reach the browser as
// SIGTERM, which Chrome answers by flushing its profile, never as SIGKILL. The
// stand-in browser records the signal it got and exits the way Chrome would.
func TestCancelAsksChromeToQuit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	marker := filepath.Join(dir, "terminated")
	browser := filepath.Join(dir, "fake-chrome")
	script := "#!/bin/sh\ntrap 'echo flushed > \"" + marker + "\"; exit 0' TERM\n" +
		"touch \"" + ready + "\"\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(browser, []byte(script), 0o700); err != nil { //nolint:gosec // test stand-in must be executable
		t.Fatalf("writing fake browser: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- launchClean(ctx, Options{UserDataDir: dir}, browser, "") }()

	// Signal only once the trap is installed: macOS can take a moment to start a
	// freshly written executable, and an early SIGTERM would test nothing.
	waitForFile(t, ready)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a cancelled session is a normal end, got %v", err)
		}
	case <-time.After(shutdownGrace):
		t.Fatal("launchClean did not return after cancel")
	}

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("browser was not asked to quit (no SIGTERM handled): %v", err)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()

	deadline := time.Now().Add(shutdownGrace)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fake browser never became ready: %s", path)
}
