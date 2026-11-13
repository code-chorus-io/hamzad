package netcheck_test

import (
	"strings"
	"testing"
	"time"

	domain "github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/geo"
	"github.com/code-chorus-io/hamzad/internal/infra/netcheck"
)

// winter is a moment when neither Berlin nor Paris observes summer time.
var winter = time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)

var berlin = geo.Location{Timezone: "Europe/Berlin", Latitude: 52.52, Longitude: 13.40}

func TestMismatchesAgreeingProfile(t *testing.T) {
	t.Parallel()

	// A different zone with the same offset, and a geolocation a few hundred
	// km away, are both what a real user behind a German exit looks like.
	p := domain.Profile{Timezone: "Europe/Paris"}
	p.Fingerprint.Geolocation = &domain.LatLng{Latitude: 50.11, Longitude: 8.68} // Frankfurt

	if got := netcheck.Mismatches(p, berlin, winter); len(got) != 0 {
		t.Fatalf("unexpected mismatches: %v", got)
	}
}

func TestMismatchesTimezoneAndDistance(t *testing.T) {
	t.Parallel()

	p := domain.Profile{Timezone: "Asia/Tokyo"}
	p.Fingerprint.Geolocation = &domain.LatLng{Latitude: 35.68, Longitude: 139.69}

	got := netcheck.Mismatches(p, berlin, winter)
	if len(got) != 2 {
		t.Fatalf("got %v, want a timezone and a distance mismatch", got)
	}
	if !strings.Contains(got[0], "UTC+09:00") || !strings.Contains(got[0], "UTC+01:00") {
		t.Errorf("timezone mismatch = %q", got[0])
	}
	if !strings.Contains(got[1], "km") {
		t.Errorf("distance mismatch = %q", got[1])
	}
}

func TestMismatchesNoTimezonePinned(t *testing.T) {
	t.Parallel()

	got := netcheck.Mismatches(domain.Profile{}, berlin, winter)
	if len(got) != 1 || !strings.Contains(got[0], "no timezone pinned") {
		t.Fatalf("got %v", got)
	}
}

func TestMismatchesNegativeOffset(t *testing.T) {
	t.Parallel()

	got := netcheck.Mismatches(domain.Profile{Timezone: "America/New_York"}, berlin, winter)
	if len(got) != 1 || !strings.Contains(got[0], "UTC-05:00") {
		t.Fatalf("got %v", got)
	}
}

func TestDistanceKM(t *testing.T) {
	t.Parallel()

	// Berlin to Paris is about 878 km.
	if d := netcheck.DistanceKM(52.52, 13.40, 48.86, 2.35); d < 860 || d > 895 {
		t.Fatalf("Berlin–Paris = %.0f km", d)
	}
	if d := netcheck.DistanceKM(1, 1, 1, 1); d != 0 {
		t.Fatalf("same point = %f", d)
	}
}
