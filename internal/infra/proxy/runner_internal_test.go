package proxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
)

// errBoom stands in for any start failure that is not a lost port.
var errBoom = errors.New("boom")

// busyBindError returns the real error the kernel gives for binding a port that
// is already listening, so the classifier is checked against the actual error
// chain rather than a hand-built one.
func busyBindError(t *testing.T) error {
	t.Helper()

	lc := &net.ListenConfig{}

	held, err := lc.Listen(t.Context(), "tcp", loopback+":0")
	if err != nil {
		t.Fatalf("holding a port: %v", err)
	}
	defer func() { _ = held.Close() }()

	second, err := lc.Listen(t.Context(), "tcp", held.Addr().String())
	if err == nil {
		_ = second.Close()
		t.Fatal("binding an occupied port succeeded")
	}

	// Wrapped, not returned bare: %w keeps the syscall errno reachable, which is
	// the whole point of handing this to the classifier.
	return fmt.Errorf("rebinding an occupied port: %w", err)
}

func TestIsAddrInUse(t *testing.T) {
	t.Parallel()

	if got := isAddrInUse(busyBindError(t)); !got {
		t.Error("isAddrInUse(occupied port) = false, want true")
	}
	if got := isAddrInUse(errBoom); got {
		t.Error("isAddrInUse(unrelated error) = true, want false")
	}
	if got := isAddrInUse(context.Canceled); got {
		t.Error("isAddrInUse(context.Canceled) = true, want false")
	}
}

// TestStartWithRetryOutlastsALostPort is the regression guard for the CI flake:
// a relay whose port was taken between reserving and binding it must come back
// on a fresh port rather than failing the launch.
func TestStartWithRetryOutlastsALostPort(t *testing.T) {
	t.Parallel()

	busy := busyBindError(t)
	calls := 0
	attempt := func(context.Context, Outbound) (*Relay, error) {
		calls++
		if calls < startAttempts {
			return nil, busy
		}

		return &Relay{addr: "127.0.0.1:1"}, nil
	}

	relay, err := startWithRetry(t.Context(), Outbound{}, attempt)
	if err != nil {
		t.Fatalf("startWithRetry: %v", err)
	}
	if relay.Addr() != "127.0.0.1:1" {
		t.Errorf("Addr = %q, want the relay from the successful attempt", relay.Addr())
	}
	if calls != startAttempts {
		t.Errorf("attempts = %d, want %d", calls, startAttempts)
	}
}

// TestStartWithRetryDoesNotMaskRealFailures keeps the retry narrow: a bad
// outbound has to fail on the first attempt, not after three identical ones.
func TestStartWithRetryDoesNotMaskRealFailures(t *testing.T) {
	t.Parallel()

	calls := 0
	attempt := func(context.Context, Outbound) (*Relay, error) {
		calls++

		return nil, errBoom
	}

	_, err := startWithRetry(t.Context(), Outbound{}, attempt)
	if !errors.Is(err, errBoom) {
		t.Fatalf("error = %v, want errBoom", err)
	}
	if calls != 1 {
		t.Errorf("attempts = %d, want 1", calls)
	}
}

// TestStartWithRetryGivesUp reports a port it never managed to hold, rather
// than retrying forever.
func TestStartWithRetryGivesUp(t *testing.T) {
	t.Parallel()

	busy := busyBindError(t)
	calls := 0
	attempt := func(context.Context, Outbound) (*Relay, error) {
		calls++

		return nil, busy
	}

	_, err := startWithRetry(t.Context(), Outbound{}, attempt)
	if !errors.Is(err, errPortRace) {
		t.Fatalf("error = %v, want errPortRace", err)
	}
	// The cause survives, so the report names the port that was lost.
	if !isAddrInUse(err) {
		t.Error("the underlying bind failure was dropped from the error chain")
	}
	if calls != startAttempts {
		t.Errorf("attempts = %d, want %d", calls, startAttempts)
	}
}
