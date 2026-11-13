package cookiefmt_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/code-chorus-io/hamzad/internal/infra/chrome"
	"github.com/code-chorus-io/hamzad/internal/infra/cookiefmt"
)

const (
	dotExample = ".example.com"
	appExample = "app.example.com"
)

func mustRead(t *testing.T, in string) []chrome.Cookie {
	t.Helper()

	got, err := cookiefmt.Read([]byte(in))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	return got
}

func TestReadCookieEditor(t *testing.T) {
	t.Parallel()

	got := mustRead(t, `[
	  {"domain":"example.com","hostOnly":false,"name":"a","value":"1","path":"/","secure":true,
	   "httpOnly":true,"sameSite":"no_restriction","session":false,"expirationDate":1900000000.5,"storeId":"0"},
	  {"domain":".app.example.com","hostOnly":true,"name":"b","value":"2","path":"/x","sameSite":"unspecified","session":true}
	]`)

	if len(got) != 2 {
		t.Fatalf("got %d cookies", len(got))
	}
	want := []chrome.Cookie{
		{Name: "a", Value: "1", Domain: dotExample, Path: "/", Secure: true, HTTPOnly: true, SameSite: "None", Expires: 1900000000.5},
		{Name: "b", Value: "2", Domain: appExample, Path: "/x", Session: true},
	}
	for i := range want {
		assertCookie(t, got[i], want[i], true)
	}
}

func TestReadDevToolsAndGoLoginNumbers(t *testing.T) {
	t.Parallel()

	got := mustRead(t, `[
	  {"name":"a","value":"1","domain":"example.com","path":"/","expires":-1,"sameSite":"Lax"},
	  {"name":"b","value":"2","domain":".example.com","expires":1900000000,"sameSite":2},
	  {"name":"c","value":"3","url":"https://shop.example.com:8443/cart?x=1"}
	]`)

	if got[0].Domain != "example.com" || !got[0].Session || got[0].SameSite != "Lax" {
		t.Errorf("a = %+v", got[0])
	}
	if got[1].Session || got[1].SameSite != "Strict" || got[1].Path != "/" {
		t.Errorf("b = %+v", got[1])
	}
	if c := got[2]; c.Domain != "shop.example.com" || c.Path != "/cart" || !c.Secure {
		t.Errorf("c = %+v", c)
	}
}

func TestReadPlaywrightStorageState(t *testing.T) {
	t.Parallel()

	got := mustRead(t, `{"cookies":[{"name":"a","value":"1","domain":"example.com","path":"/","expires":1900000000,
	  "httpOnly":false,"secure":true,"sameSite":"None"}],"origins":[]}`)
	if len(got) != 1 || got[0].SameSite != "None" || got[0].Session {
		t.Fatalf("got %+v", got)
	}
}

func TestReadHamzadExport(t *testing.T) {
	t.Parallel()

	got := mustRead(t, `{"format":1,"cookies":[{"name":"a","value":"1","domain":".example.com","path":"/","expires":1900000000}]}`)
	if len(got) != 1 || got[0].Domain != dotExample {
		t.Fatalf("got %+v", got)
	}
}

func TestReadNetscape(t *testing.T) {
	t.Parallel()

	got := mustRead(t, "# Netscape HTTP Cookie File\n\n"+
		".example.com\tTRUE\t/\tTRUE\t1900000000\tsid\tabc\n"+
		"#HttpOnly_app.example.com\tFALSE\t/api\tFALSE\t0\ttok\txyz\r\n")

	if len(got) != 2 {
		t.Fatalf("got %d cookies", len(got))
	}
	if c := got[0]; c.Domain != dotExample || !c.Secure || c.Session || c.HTTPOnly {
		t.Errorf("sid = %+v", c)
	}
	if c := got[1]; c.Domain != appExample || !c.HTTPOnly || !c.Session || c.Path != "/api" {
		t.Errorf("tok = %+v", c)
	}
}

func TestReadRejectsGarbage(t *testing.T) {
	t.Parallel()

	if _, err := cookiefmt.Read([]byte("hello world")); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := cookiefmt.Read([]byte(`[{"name":"a","value":"1"}]`)); err == nil {
		t.Fatal("expected an error for a cookie with no domain")
	}
}

// TestRoundTrip checks that every output format reads back to the same cookies.
func TestRoundTrip(t *testing.T) {
	t.Parallel()

	in := []chrome.Cookie{
		{Name: "a", Value: "1", Domain: dotExample, Path: "/", Expires: 1900000000, Secure: true, HTTPOnly: true, SameSite: "Lax"},
		{Name: "b", Value: "2", Domain: appExample, Path: "/x", Session: true},
	}

	for _, f := range cookiefmt.Formats {
		t.Run(string(f), func(t *testing.T) {
			t.Parallel()

			out, err := cookiefmt.Write(in, f)
			if err != nil {
				t.Fatalf("Write: %v", err)
			}
			got := mustRead(t, string(out))
			if len(got) != len(in) {
				t.Fatalf("got %d cookies", len(got))
			}
			for i := range in {
				// cookies.txt has no SameSite column.
				assertCookie(t, got[i], in[i], f != cookiefmt.FormatNetscape)
			}
		})
	}
}

func TestWriteUnknownFormat(t *testing.T) {
	t.Parallel()

	_, err := cookiefmt.Write(nil, "xml")
	if err == nil || !strings.Contains(err.Error(), "xml") {
		t.Fatalf("err = %v", err)
	}
}

// assertCookie compares the fields every format carries, and SameSite when
// withSameSite is set.
func assertCookie(t *testing.T, got, want chrome.Cookie, withSameSite bool) {
	t.Helper()

	if !withSameSite {
		got.SameSite, want.SameSite = "", ""
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
