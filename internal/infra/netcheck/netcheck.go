// Package netcheck tests a profile's proxy without a browser: does it connect,
// how slow is it, where does it come out, and does that agree with what the
// profile claims about itself.
//
// The test runs through the same sing-box relay Chrome would use, so it speaks
// every protocol a profile can, and what it measures is what a site would see.
package netcheck

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"slices"
	"time"

	domain "github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/geo"
	"github.com/code-chorus-io/hamzad/internal/infra/proxy"
)

// LatencyURL answers with an empty 204 and is served from nearby almost
// everywhere, so the time it takes is the proxy's, not the server's.
const LatencyURL = "https://www.gstatic.com/generate_204"

// latencySamples is how many round trips are timed after the first.
const latencySamples = 3

// requestTimeout bounds each request through the proxy.
const requestTimeout = 15 * time.Second

// FarKM is the distance between a profile's pinned geolocation and its exit
// beyond which they are reported as disagreeing. IP geolocation is coarse —
// often the provider's city, not the user's — so this is generous.
const FarKM = 500

// Result is what one check found.
type Result struct {
	// Exit is where the traffic comes out.
	Exit geo.Location
	// Connect is the first request: relay dial, proxy handshake, TLS and one
	// round trip. It is what a fresh page load pays.
	Connect time.Duration
	// RTT is the median of the requests after it, on the open connection.
	RTT time.Duration
}

// Check runs the test for one proxy spec (a share link or sing-box outbound;
// "" is a direct connection).
func Check(ctx context.Context, spec string) (Result, error) {
	var res Result

	err := proxy.WithRelay(ctx, spec, func(proxyURL *url.URL) error {
		connect, rtt, err := latency(ctx, proxyURL)
		if err != nil {
			return err
		}
		res.Connect, res.RTT = connect, rtt

		loc, err := geo.Lookup(ctx, proxyURL)
		if err != nil {
			return err
		}
		res.Exit = loc

		return nil
	})

	return res, err
}

// latency times one request on a fresh connection, then latencySamples more on
// the same one, and returns the first and the median of the rest.
func latency(ctx context.Context, proxyURL *url.URL) (time.Duration, time.Duration, error) {
	transport := &http.Transport{}
	if proxyURL != nil {
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: requestTimeout, Transport: transport}

	connect, err := timeRequest(ctx, client)
	if err != nil {
		return 0, 0, err
	}

	samples := make([]time.Duration, 0, latencySamples)
	for range latencySamples {
		d, err := timeRequest(ctx, client)
		if err != nil {
			return 0, 0, err
		}
		samples = append(samples, d)
	}
	slices.Sort(samples)

	return connect, samples[len(samples)/2], nil
}

func timeRequest(ctx context.Context, client *http.Client) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, LatencyURL, nil)
	if err != nil {
		return 0, fmt.Errorf("building request: %w", err)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request through proxy: %w", err)
	}
	_ = resp.Body.Close()

	return time.Since(start), nil
}

// Mismatches lists where the profile's own settings disagree with its exit:
// the things a site can compare for free. at is the moment the timezone
// offsets are compared at.
func Mismatches(p domain.Profile, exit geo.Location, at time.Time) []string {
	var out []string

	if msg := timezoneMismatch(p.Timezone, exit.Timezone, at); msg != "" {
		out = append(out, msg)
	}

	if g := p.Fingerprint.Geolocation; g != nil && (exit.Latitude != 0 || exit.Longitude != 0) {
		if km := DistanceKM(g.Latitude, g.Longitude, exit.Latitude, exit.Longitude); km > FarKM {
			out = append(out, fmt.Sprintf("geolocation is %.0f km from the exit IP's location", km))
		}
	}

	return out
}

// timezoneMismatch compares UTC offsets rather than names: Europe/Berlin and
// Europe/Paris are different zones that no page can tell apart today, while
// the same name everywhere is not required.
func timezoneMismatch(profileTZ, exitTZ string, at time.Time) string {
	if exitTZ == "" {
		return ""
	}
	if profileTZ == "" {
		return fmt.Sprintf("no timezone pinned, so the browser shows this machine's — the exit IP is in %s "+
			"(run 'profile geo' to align them)", exitTZ)
	}

	pl, err := time.LoadLocation(profileTZ)
	if err != nil {
		return fmt.Sprintf("timezone %q is not a known zone", profileTZ)
	}
	el, err := time.LoadLocation(exitTZ)
	if err != nil {
		return ""
	}

	_, po := at.In(pl).Zone()
	_, eo := at.In(el).Zone()
	if po != eo {
		return fmt.Sprintf("timezone %s (UTC%s) does not match the exit IP's %s (UTC%s)",
			profileTZ, offset(po), exitTZ, offset(eo))
	}

	return ""
}

func offset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}

	return fmt.Sprintf("%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

// earthRadiusKM is the mean radius of the Earth.
const earthRadiusKM = 6371

// DistanceKM is the great-circle distance between two points, in kilometres.
func DistanceKM(lat1, lng1, lat2, lng2 float64) float64 {
	rad := func(d float64) float64 { return d * math.Pi / 180 }

	dLat := rad(lat2 - lat1)
	dLng := rad(lng2 - lng1)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(rad(lat1))*math.Cos(rad(lat2))*math.Sin(dLng/2)*math.Sin(dLng/2)

	return 2 * earthRadiusKM * math.Asin(math.Sqrt(h))
}
