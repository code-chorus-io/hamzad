// Package cookiefmt reads and writes cookies in the formats other tools use, so
// a profile's cookies can come from — or go to — something other than hamzad.
//
// Read accepts, and tells apart by their shape:
//
//   - Cookie-Editor / EditThisCookie / GoLogin JSON: an array of objects with
//     "expirationDate" (Unix seconds), "hostOnly", "session" and a lower-case
//     "sameSite" ("no_restriction", "lax", "strict", "unspecified").
//   - DevTools / Puppeteer / Playwright JSON: an array of objects with
//     "expires" (Unix seconds, -1 for a session cookie) and "sameSite" as
//     "Strict", "Lax" or "None".
//   - Playwright storageState: an object whose "cookies" array is the above.
//   - hamzad's own bundle export ({"format":1,"cookies":[…]}).
//   - Netscape cookies.txt, as written by curl, wget and yt-dlp.
//
// Write produces the Cookie-Editor JSON, the DevTools JSON, or cookies.txt.
package cookiefmt

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/code-chorus-io/hamzad/internal/infra/chrome"
)

// Format names an output format.
type Format string

// The output formats.
const (
	// FormatJSON is the Cookie-Editor / EditThisCookie / GoLogin array, the one
	// most cookie tools import.
	FormatJSON Format = "json"
	// FormatCDP is the DevTools array Puppeteer's page.setCookie and
	// Playwright's context.addCookies take.
	FormatCDP Format = "cdp"
	// FormatNetscape is cookies.txt.
	FormatNetscape Format = "netscape"
)

// Formats lists the output formats, for help text.
var Formats = []Format{FormatJSON, FormatCDP, FormatNetscape}

// ErrUnknownFormat is returned for an output format Write does not know.
var ErrUnknownFormat = errors.New("unknown cookie format")

// ErrUnrecognised is returned when Read cannot tell what the input is.
var ErrUnrecognised = errors.New("unrecognised cookie file: expected a JSON array or object, or Netscape cookies.txt")

// errNoDomain is returned for a cookie with neither a domain nor a URL.
var errNoDomain = errors.New("cookie has no domain")

// sameSite values, in DevTools spelling.
const (
	sameSiteStrict = "Strict"
	sameSiteLax    = "Lax"
	sameSiteNone   = "None"
)

// Read parses cookies in any of the formats in the package doc.
func Read(data []byte) ([]chrome.Cookie, error) {
	trimmed := bytes.TrimSpace(data)
	trimmed = bytes.TrimPrefix(trimmed, []byte("\xef\xbb\xbf")) // a UTF-8 BOM

	switch {
	case len(trimmed) == 0:
		return nil, nil
	case trimmed[0] == '[':
		return readArray(trimmed)
	case trimmed[0] == '{':
		return readObject(trimmed)
	default:
		return readNetscape(trimmed)
	}
}

// readObject handles the two object shapes: hamzad's export and Playwright's
// storageState. Both keep the cookies under "cookies".
func readObject(data []byte) ([]chrome.Cookie, error) {
	var doc struct {
		Format  *int            `json:"format"`
		Cookies json.RawMessage `json:"cookies"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decoding cookie file: %w", err)
	}
	if doc.Format != nil {
		return chrome.DecodeCookies(data)
	}
	if len(doc.Cookies) == 0 {
		return nil, ErrUnrecognised
	}

	return readArray(doc.Cookies)
}

// anyCookie is the union of the JSON shapes; which fields are present decides
// how each is read.
type anyCookie struct {
	Name           string          `json:"name"`
	Value          string          `json:"value"`
	Domain         string          `json:"domain"`
	URL            string          `json:"url"`
	Path           string          `json:"path"`
	Secure         bool            `json:"secure"`
	HTTPOnly       bool            `json:"httpOnly"`
	SameSite       json.RawMessage `json:"sameSite"`
	Expires        *float64        `json:"expires"`
	ExpirationDate *float64        `json:"expirationDate"`
	Session        *bool           `json:"session"`
	HostOnly       *bool           `json:"hostOnly"`
	Priority       string          `json:"priority"`
	PartitionKey   json.RawMessage `json:"partitionKey"`
}

func readArray(data []byte) ([]chrome.Cookie, error) {
	var in []anyCookie
	if err := json.Unmarshal(data, &in); err != nil {
		return nil, fmt.Errorf("decoding cookie array: %w", err)
	}

	out := make([]chrome.Cookie, 0, len(in))
	for i, a := range in {
		c, err := a.cookie()
		if err != nil {
			return nil, fmt.Errorf("cookie %d (%q): %w", i, a.Name, err)
		}
		out = append(out, c)
	}

	return out, nil
}

func (a anyCookie) cookie() (chrome.Cookie, error) {
	c := chrome.Cookie{
		Name:     a.Name,
		Value:    a.Value,
		Domain:   a.Domain,
		Path:     a.Path,
		Secure:   a.Secure,
		HTTPOnly: a.HTTPOnly,
		SameSite: normalizeSameSite(a.SameSite),
		Priority: a.Priority,
	}
	if len(a.PartitionKey) > 0 && string(a.PartitionKey) != "null" {
		c.PartitionKey = a.PartitionKey
	}

	if c.Domain == "" && a.URL != "" {
		host, path, secure := splitURL(a.URL)
		c.Domain = host
		if c.Path == "" {
			c.Path = path
		}
		c.Secure = c.Secure || secure
	}
	if c.Domain == "" {
		return chrome.Cookie{}, errNoDomain
	}
	if c.Path == "" {
		c.Path = "/"
	}

	// hostOnly, where a format carries it, decides; the domain's leading dot is
	// only DevTools' way of saying the same thing.
	if a.HostOnly != nil {
		c.Domain = withHostOnly(c.Domain, *a.HostOnly)
	}

	c.Session, c.Expires = expiry(a)

	return c, nil
}

// expiry works out whether a cookie is a session cookie and when it expires.
func expiry(a anyCookie) (bool, float64) {
	var at float64
	switch {
	case a.ExpirationDate != nil:
		at = *a.ExpirationDate
	case a.Expires != nil:
		at = *a.Expires
	}

	if a.Session != nil && *a.Session {
		return true, 0
	}
	if at <= 0 {
		return true, 0
	}

	return false, at
}

// normalizeSameSite maps every spelling in use onto DevTools' — strings in any
// case, Cookie-Editor's "no_restriction"/"unspecified", and GoLogin's numbers
// (-1 unspecified, 0 none, 1 lax, 2 strict).
func normalizeSameSite(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var n int
	if json.Unmarshal(raw, &n) == nil {
		return numericSameSite(n)
	}

	var str string
	if json.Unmarshal(raw, &str) != nil {
		return ""
	}

	return namedSameSite(str)
}

// numericSameSite reads GoLogin's numeric encoding.
func numericSameSite(n int) string {
	switch n {
	case 0:
		return sameSiteNone
	case 1:
		return sameSiteLax
	case 2: //nolint:mnd // GoLogin's numeric encoding
		return sameSiteStrict
	default:
		return ""
	}
}

// namedSameSite reads every string spelling, in any case.
func namedSameSite(s string) string {
	switch strings.ToLower(s) {
	case "strict":
		return sameSiteStrict
	case "lax":
		return sameSiteLax
	case "none", "no_restriction":
		return sameSiteNone
	default:
		return ""
	}
}

// withHostOnly spells domain the DevTools way: no leading dot for host-only.
func withHostOnly(domain string, hostOnly bool) string {
	bare := strings.TrimPrefix(domain, ".")
	if hostOnly {
		return bare
	}

	return "." + bare
}

// splitURL pulls the host, path and secure flag out of a cookie's URL.
func splitURL(raw string) (string, string, bool) {
	secure := strings.HasPrefix(raw, "https://")
	rest := raw
	if _, after, ok := strings.Cut(raw, "://"); ok {
		rest = after
	}

	host, path := rest, "/"
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host, path = rest[:i], rest[i:]
	}
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.HasSuffix(host, "]") {
		host = host[:i]
	}

	return host, path, secure
}

// netscapeHTTPOnly is the prefix curl puts on the domain of an HttpOnly cookie.
const netscapeHTTPOnly = "#HttpOnly_"

// netscapeFields is the column count of a cookies.txt line.
const netscapeFields = 7

func readNetscape(data []byte) ([]chrome.Cookie, error) {
	var out []chrome.Cookie

	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for line := 1; scan.Scan(); line++ {
		text := strings.TrimRight(scan.Text(), "\r")

		httpOnly := false
		if rest, ok := strings.CutPrefix(text, netscapeHTTPOnly); ok {
			text, httpOnly = rest, true
		}
		if strings.TrimSpace(text) == "" || strings.HasPrefix(text, "#") {
			continue
		}

		f := strings.Split(text, "\t")
		if len(f) != netscapeFields {
			return nil, fmt.Errorf("%w (line %d has %d tab-separated fields, want %d)",
				ErrUnrecognised, line, len(f), netscapeFields)
		}

		expires, err := strconv.ParseFloat(f[4], 64)
		if err != nil {
			return nil, fmt.Errorf("cookies.txt line %d: bad expiry %q: %w", line, f[4], err)
		}

		c := chrome.Cookie{
			Domain:   withHostOnly(f[0], !strings.EqualFold(f[1], "TRUE")),
			Path:     f[2],
			Secure:   strings.EqualFold(f[3], "TRUE"),
			Name:     f[5],
			Value:    f[6],
			HTTPOnly: httpOnly,
		}
		if expires <= 0 {
			c.Session = true
		} else {
			c.Expires = expires
		}
		out = append(out, c)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("reading cookies.txt: %w", err)
	}
	if out == nil {
		out = []chrome.Cookie{}
	}

	return out, nil
}

// Write renders cookies in the named format.
func Write(cookies []chrome.Cookie, format Format) ([]byte, error) {
	switch format {
	case FormatJSON:
		return writeEditor(cookies)
	case FormatCDP:
		return writeCDP(cookies)
	case FormatNetscape:
		return writeNetscape(cookies), nil
	default:
		return nil, fmt.Errorf("%w: %q (want one of %v)", ErrUnknownFormat, format, Formats)
	}
}

// editorCookie is one entry of the Cookie-Editor array.
type editorCookie struct {
	Domain         string   `json:"domain"`
	ExpirationDate *float64 `json:"expirationDate,omitempty"`
	HostOnly       bool     `json:"hostOnly"`
	HTTPOnly       bool     `json:"httpOnly"`
	Name           string   `json:"name"`
	Path           string   `json:"path"`
	SameSite       string   `json:"sameSite"`
	Secure         bool     `json:"secure"`
	Session        bool     `json:"session"`
	StoreID        *string  `json:"storeId"`
	Value          string   `json:"value"`
}

func writeEditor(cookies []chrome.Cookie) ([]byte, error) {
	out := make([]editorCookie, 0, len(cookies))
	for _, c := range cookies {
		e := editorCookie{
			Domain:   c.Domain,
			HostOnly: !strings.HasPrefix(c.Domain, "."),
			HTTPOnly: c.HTTPOnly,
			Name:     c.Name,
			Path:     c.Path,
			SameSite: editorSameSite(c.SameSite),
			Secure:   c.Secure,
			Session:  isSession(c),
			Value:    c.Value,
		}
		if !e.Session {
			at := c.Expires
			e.ExpirationDate = &at
		}
		out = append(out, e)
	}

	return marshal(out)
}

func editorSameSite(s string) string {
	switch s {
	case sameSiteStrict:
		return "strict"
	case sameSiteLax:
		return "lax"
	case sameSiteNone:
		return "no_restriction"
	default:
		return "unspecified"
	}
}

// cdpCookie is one entry of the DevTools array.
type cdpCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   string  `json:"domain"`
	Path     string  `json:"path"`
	Expires  float64 `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
	SameSite string  `json:"sameSite,omitempty"`
}

func writeCDP(cookies []chrome.Cookie) ([]byte, error) {
	out := make([]cdpCookie, 0, len(cookies))
	for _, c := range cookies {
		expires := c.Expires
		if isSession(c) {
			expires = -1
		}
		out = append(out, cdpCookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: expires, HTTPOnly: c.HTTPOnly, Secure: c.Secure, SameSite: c.SameSite,
		})
	}

	return marshal(out)
}

func writeNetscape(cookies []chrome.Cookie) []byte {
	var b strings.Builder
	b.WriteString("# Netscape HTTP Cookie File\n# Written by hamzad. This file holds live session secrets.\n\n")

	for _, c := range cookies {
		domain := c.Domain
		if c.HTTPOnly {
			domain = netscapeHTTPOnly + domain
		}
		var expires int64
		if !isSession(c) {
			expires = int64(math.Round(c.Expires))
		}
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			domain, upperBool(strings.HasPrefix(c.Domain, ".")), c.Path, upperBool(c.Secure), expires, c.Name, c.Value)
	}

	return []byte(b.String())
}

func isSession(c chrome.Cookie) bool {
	return c.Session || c.Expires <= 0
}

func upperBool(b bool) string {
	if b {
		return "TRUE"
	}

	return "FALSE"
}

func marshal(v any) ([]byte, error) {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encoding cookies: %w", err)
	}

	return append(out, '\n'), nil
}
