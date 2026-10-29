package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// TestSaveLeavesUnsetNumbersOut keeps profiles.toml free of `= 0` lines for
// fields nobody set. The file is committed and shared, so every spurious line
// is diff noise, and BurntSushi's omitempty does not skip zero numbers — only
// omitzero does. Coordinates are the exception: 0 is a real place.
func TestSaveLeavesUnsetNumbersOut(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	p := profile.Profile{
		Name: "plain",
		Fingerprint: profile.Fingerprint{
			Geolocation: &profile.LatLng{Latitude: 41.7, Longitude: 0},
		},
	}
	if err := st.Add(p); err != nil {
		t.Fatalf("Add: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(st.Dir, "profiles.toml"))
	if err != nil {
		t.Fatalf("reading profiles.toml: %v", err)
	}
	doc := string(b)

	for _, key := range []string{"screen_width", "screen_height", "hardware_concurrency", "device_memory", "accuracy"} {
		if strings.Contains(doc, key) {
			t.Errorf("unset %s was written:\n%s", key, doc)
		}
	}
	if !strings.Contains(doc, "longitude = 0.0") {
		t.Errorf("a zero longitude must still be written:\n%s", doc)
	}

	got, err := st.Get("plain")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Fingerprint.Geolocation == nil || got.Fingerprint.Geolocation.Latitude != 41.7 {
		t.Errorf("geolocation did not round-trip: %+v", got.Fingerprint.Geolocation)
	}
}
