// Package geo resolves the timezone, coordinates, and locale of a network exit
// point by querying an IP-geolocation service. When a proxy is supplied the
// request egresses through it, so the result reflects the proxy's exit IP —
// mirroring how anti-detect browsers auto-align a profile's timezone and
// geolocation with its proxy.
package geo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Endpoint is the IP-geolocation service. ip-api.com is keyless and returns the
// IANA timezone plus coordinates and country code in one call.
const Endpoint = "http://ip-api.com/json/?fields=status,message,query,countryCode,timezone,lat,lon"

// ErrLookupFailed is returned when the geolocation service reports a failure.
var ErrLookupFailed = errors.New("geo lookup failed")

// Location is the resolved geolocation of an exit IP.
type Location struct {
	IP          string
	CountryCode string
	Timezone    string
	Latitude    float64
	Longitude   float64
}

type apiResponse struct {
	Status      string  `json:"status"`
	Message     string  `json:"message"`
	Query       string  `json:"query"`
	CountryCode string  `json:"countryCode"`
	Timezone    string  `json:"timezone"`
	Lat         float64 `json:"lat"`
	Lon         float64 `json:"lon"`
}

// Lookup resolves the exit location, routing through proxy when it is non-nil.
func Lookup(ctx context.Context, proxy *url.URL) (Location, error) {
	transport := &http.Transport{}
	if proxy != nil {
		transport.Proxy = http.ProxyURL(proxy)
	}
	client := &http.Client{Timeout: 15 * time.Second, Transport: transport}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint, nil)
	if err != nil {
		return Location{}, fmt.Errorf("building geo request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return Location{}, fmt.Errorf("querying geo service: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body apiResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Location{}, fmt.Errorf("decoding geo response: %w", err)
	}
	if body.Status != "success" {
		return Location{}, fmt.Errorf("%w: %s", ErrLookupFailed, body.Message)
	}

	return Location{
		IP:          body.Query,
		CountryCode: body.CountryCode,
		Timezone:    body.Timezone,
		Latitude:    body.Lat,
		Longitude:   body.Lon,
	}, nil
}
