package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ManifestURL lists the current Chrome for Testing build of each channel. It is
// the small per-channel manifest rather than the full known-good list, which is
// several megabytes and unnecessary when a pinned version's URL is derivable.
const ManifestURL = "https://googlechromelabs.github.io/chrome-for-testing/last-known-good-versions-with-downloads.json"

// downloadURLFormat builds the archive URL for an exact version. Chrome for
// Testing lays its bucket out predictably, so a pinned version needs no
// manifest lookup at all.
const downloadURLFormat = "https://storage.googleapis.com/chrome-for-testing-public/%s/%s/chrome-%s.zip"

// ErrChannelUnknown is returned for a channel name Chrome for Testing does not
// publish.
var ErrChannelUnknown = errors.New("unknown channel (want stable, beta, dev or canary)")

// ErrNoBuildForPlatform is returned when a resolved channel has no archive for
// the host platform.
var ErrNoBuildForPlatform = errors.New("channel publishes no build for this platform")

// manifestTimeout bounds the metadata fetch. The download itself is far longer
// and governed by the caller's context.
const manifestTimeout = 30 * time.Second

// Build identifies one downloadable browser archive.
type Build struct {
	Version  string
	Platform string
	URL      string
}

type manifest struct {
	Channels map[string]struct {
		Version   string `json:"version"`
		Downloads struct {
			Chrome []struct {
				Platform string `json:"platform"`
				URL      string `json:"url"`
			} `json:"chrome"`
		} `json:"downloads"`
	} `json:"channels"`
}

// Resolve turns a channel name ("stable", "beta", "dev", "canary") or an exact
// version ("151.0.7922.76") into a downloadable build for the host platform.
// A channel costs a network round trip; an exact version does not.
func Resolve(ctx context.Context, spec string) (Build, error) {
	platform, err := Platform()
	if err != nil {
		return Build{}, err
	}

	spec = strings.TrimSpace(spec)
	if spec == "" {
		spec = "stable"
	}

	// A leading digit means a pinned version, whose URL is derivable.
	if spec[0] >= '0' && spec[0] <= '9' {
		return Build{
			Version:  spec,
			Platform: platform,
			URL:      fmt.Sprintf(downloadURLFormat, spec, platform, platform),
		}, nil
	}

	return resolveChannel(ctx, spec, platform)
}

// resolveChannel looks a channel up in the published manifest.
func resolveChannel(ctx context.Context, channel, platform string) (Build, error) {
	man, err := fetchManifest(ctx)
	if err != nil {
		return Build{}, err
	}

	// The manifest keys channels in title case ("Stable"); accept any casing.
	entry, ok := man.Channels[strings.ToUpper(channel[:1])+strings.ToLower(channel[1:])]
	if !ok {
		return Build{}, fmt.Errorf("%w: %q", ErrChannelUnknown, channel)
	}

	for _, d := range entry.Downloads.Chrome {
		if d.Platform == platform {
			return Build{Version: entry.Version, Platform: platform, URL: d.URL}, nil
		}
	}

	return Build{}, fmt.Errorf("%w: %s on %s", ErrNoBuildForPlatform, channel, platform)
}

func fetchManifest(ctx context.Context) (manifest, error) {
	ctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ManifestURL, nil)
	if err != nil {
		return manifest{}, fmt.Errorf("building manifest request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return manifest{}, fmt.Errorf("fetching chrome-for-testing manifest: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return manifest{}, fmt.Errorf("fetching chrome-for-testing manifest: %w: %s", errBadStatus, resp.Status)
	}

	var man manifest
	if err := json.NewDecoder(resp.Body).Decode(&man); err != nil {
		return manifest{}, fmt.Errorf("decoding chrome-for-testing manifest: %w", err)
	}

	return man, nil
}
