package geo_test

import (
	"errors"
	"net/url"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/infra/geo"
)

// TestLookupRejectsUndialableProxy covers the gap between what a profile may
// carry and what the lookup can route through. The domain accepts socks4 because
// Chrome speaks it, but net/http does not dial socks4 at all — so `profile geo`
// on such a profile used to fail deep in the transport with an opaque error
// after the request was already in flight. Refuse it up front, and say what to
// do instead.
func TestLookupRejectsUndialableProxy(t *testing.T) {
	t.Parallel()

	proxy, err := url.Parse("socks4://1.2.3.4:1080")
	if err != nil {
		t.Fatalf("parsing proxy: %v", err)
	}

	_, err = geo.Lookup(t.Context(), proxy)
	if !errors.Is(err, geo.ErrProxySchemeUnsupported) {
		t.Fatalf("Lookup through socks4 = %v, want ErrProxySchemeUnsupported", err)
	}
}

// TestEndpointIsHTTPS pins the transport of the lookup. The query travels
// through the very proxy whose exit it is measuring, so over plain HTTP the
// proxy operator could rewrite the response and pick the timezone and
// coordinates the profile then pins to itself.
func TestEndpointIsHTTPS(t *testing.T) {
	t.Parallel()

	u, err := url.Parse(geo.Endpoint)
	if err != nil {
		t.Fatalf("parsing endpoint: %v", err)
	}
	if u.Scheme != "https" {
		t.Errorf("geo endpoint scheme = %q, want https", u.Scheme)
	}
}
