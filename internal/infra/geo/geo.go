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

// Endpoint is the IP-geolocation service. ipwho.is is keyless and returns the
// IANA timezone plus coordinates and country code in one call.
//
// It is queried over HTTPS deliberately. The whole point of the lookup is to ask
// what the world sees at the proxy's exit, and the question travels through that
// very proxy — so over plain HTTP the proxy operator could rewrite the answer and
// choose the timezone and coordinates the profile then pins. TLS reduces that to
// needing a certificate the local trust store accepts.
const Endpoint = "https://ipwho.is/?fields=ip,success,message,country_code,latitude,longitude,timezone"

// ErrLookupFailed is returned when the geolocation service reports a failure.
var ErrLookupFailed = errors.New("geo lookup failed")

// ErrProxySchemeUnsupported is returned for a proxy the HTTP client cannot dial.
var ErrProxySchemeUnsupported = errors.New("geo lookup cannot route through this proxy scheme")

// Location is the resolved geolocation of an exit IP.
type Location struct {
	IP          string
	CountryCode string
	Timezone    string
	Latitude    float64
	Longitude   float64
}

type apiResponse struct {
	Success     bool    `json:"success"`
	Message     string  `json:"message"`
	IP          string  `json:"ip"`
	CountryCode string  `json:"country_code"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	// Timezone is an object; only its IANA id matters here.
	Timezone struct {
		ID string `json:"id"`
	} `json:"timezone"`
}

// Lookup resolves the exit location, routing through proxy when it is non-nil.
func Lookup(ctx context.Context, proxy *url.URL) (Location, error) {
	transport := &http.Transport{}
	if proxy != nil {
		// net/http dials http, https, socks5 and socks5h proxies; socks4 has no
		// support at all and would otherwise surface as an opaque transport
		// error at request time.
		if _, ok := dialableSchemes[proxy.Scheme]; !ok {
			return Location{}, fmt.Errorf(
				"%w: %q (the browser can still use it; run 'profile geo' without a socks4 proxy, or set the timezone by hand)",
				ErrProxySchemeUnsupported, proxy.Scheme)
		}
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
	if !body.Success {
		return Location{}, fmt.Errorf("%w: %s", ErrLookupFailed, body.Message)
	}

	return Location{
		IP:          body.IP,
		CountryCode: body.CountryCode,
		Timezone:    body.Timezone.ID,
		Latitude:    body.Latitude,
		Longitude:   body.Longitude,
	}, nil
}

// dialableSchemes are the proxy transports net/http can route a request through.
var dialableSchemes = map[string]struct{}{
	"http":    {},
	"https":   {},
	"socks5":  {},
	"socks5h": {},
}
