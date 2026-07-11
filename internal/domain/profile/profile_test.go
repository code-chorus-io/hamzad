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
		{"authed socks4 proxy", profile.Profile{Name: "p", Proxy: "socks4://user:pass@1.2.3.4:1080"}, true},
		{"bad proxy scheme", profile.Profile{Name: "p", Proxy: "ftp://host:21"}, true},
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

func TestParseProxyCredentials(t *testing.T) {
	t.Parallel()

	u, err := profile.ParseProxy("http://alice:s3cret@proxy.example:8080")
	if err != nil {
		t.Fatalf("ParseProxy() unexpected error: %v", err)
	}

	if u.User == nil {
		t.Fatal("expected credentials to be parsed")
	}
	if got := u.User.Username(); got != "alice" {
		t.Errorf("username = %q, want alice", got)
	}
	if pass, _ := u.User.Password(); pass != "s3cret" {
		t.Errorf("password = %q, want s3cret", pass)
	}
}
