package profile_test

import (
	"testing"

	"github.com/1995parham/koochooloologin/internal/domain/profile"
)

func TestProfileValidate(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		profile profile.Profile
		wantErr bool
	}{
		{"minimal", profile.Profile{Name: "work"}, false},
		{"dashes and digits", profile.Profile{Name: "acct-01_x"}, false},
		{"empty name", profile.Profile{Name: ""}, true},
		{"space in name", profile.Profile{Name: "my profile"}, true},
		{"good socks proxy", profile.Profile{Name: "p", Proxy: "socks5://1.2.3.4:1080"}, false},
		//nolint:gosec // fixture proxy URL, not a real credential
		{"good authed http proxy", profile.Profile{Name: "p", Proxy: "http://user:pass@1.2.3.4:8080"}, false},
		{"authed socks5 proxy (relayed)", profile.Profile{Name: "p", Proxy: "socks5://user:pass@1.2.3.4:1080"}, false},
		// An unknown scheme is shaped like a URL, so the domain accepts it; the
		// proxy layer is what knows which protocols exist and rejects it there.
		{"unknown scheme passes the shape check", profile.Profile{Name: "p", Proxy: "ftp://host:21"}, false},
		{"proxy without host", profile.Profile{Name: "p", Proxy: "http://"}, true},
		{"good timezone", profile.Profile{Name: "p", Timezone: "Europe/Berlin"}, false},
		{"bad timezone", profile.Profile{Name: "p", Timezone: "Mars/Olympus"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := tc.profile.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// TestValidateProxySpecAcceptsBothShapes covers what a profile may now carry: a
// share link for any protocol, and a raw sing-box outbound object for the ones
// with no link form. The domain check is deliberately shallow — which protocols
// actually work is the proxy layer's business, and it is tested there — so this
// pins only that neither shape is rejected out of hand.
func TestValidateProxySpecAcceptsBothShapes(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{
		"",
		"socks5://user:pass@1.2.3.4:1080",
		"http://proxy.example:8080",
		"vless://uuid@example.com:443?security=reality",
		"trojan://pw@example.com:443",
		`{"type":"hysteria2","server":"h.example.com","server_port":443}`,
	} {
		if err := profile.ValidateProxySpec(ok); err != nil {
			t.Errorf("ValidateProxySpec(%q) = %v, want nil", ok, err)
		}
	}
}

// TestValidateProxySpecRejectsShapelessValues keeps a value that is neither a
// URL nor JSON from being stored and failing much later, at launch.
func TestValidateProxySpecRejectsShapelessValues(t *testing.T) {
	t.Parallel()

	for _, bad := range []string{"1.2.3.4:1080", "just-some-text", "socks5://"} {
		if err := profile.ValidateProxySpec(bad); err == nil {
			t.Errorf("ValidateProxySpec(%q) = nil, want an error", bad)
		}
	}
}
