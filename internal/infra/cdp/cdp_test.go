package cdp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/code-chorus-io/hamzad/internal/infra/cdp"
)

// fakeBrowser is a DevTools endpoint that answers commands and, crucially,
// records every method it was asked to run. Testing which commands are *not*
// sent is the whole point, and it needs no real Chrome.
type fakeBrowser struct {
	srv *httptest.Server

	mu      sync.Mutex
	methods []string
}

func newFakeBrowser(t *testing.T) *fakeBrowser {
	t.Helper()

	f := &fakeBrowser{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}

			var req struct {
				ID     int64  `json:"id"`
				Method string `json:"method"`
			}
			if err := json.Unmarshal(data, &req); err != nil {
				return
			}

			f.mu.Lock()
			f.methods = append(f.methods, req.Method)
			f.mu.Unlock()

			if err := conn.Write(r.Context(), websocket.MessageText, f.replyTo(req.ID, req.Method)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeBrowser) replyTo(id int64, method string) []byte {
	var result string
	switch method {
	case "Target.getTargets":
		result = `{"targetInfos":[{"targetId":"T1","type":"page"}]}`
	case "Target.attachToTarget":
		result = `{"sessionId":"S1"}`
	default:
		result = `{}`
	}

	return []byte(`{"id":` + itoa(id) + `,"result":` + result + `}`)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}

	return string(b)
}

func (f *fakeBrowser) sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.methods)
}

func (f *fakeBrowser) wsURL() string {
	return "ws" + strings.TrimPrefix(f.srv.URL, "http")
}

// TestAttachNeverEnablesRuntime is the reason this package exists.
//
// A general-purpose driver calls Runtime.enable while attaching, to tell a
// worker target from a page. Anti-bot services probe for exactly that —
// enabling the domain changes observable behaviour in the page — and it cannot
// be switched off through those libraries' APIs. Attaching here must not send
// it, nor enable any domain at all: the launcher enables Page later, once, and
// only because the injected script does not run without it.
func TestAttachNeverEnablesRuntime(t *testing.T) {
	t.Parallel()

	f := newFakeBrowser(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	client, err := cdp.Dial(ctx, f.wsURL())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.AttachToPage(ctx); err != nil {
		t.Fatalf("AttachToPage: %v", err)
	}

	for _, method := range f.sent() {
		if strings.HasSuffix(method, ".enable") {
			t.Errorf("attaching enabled a domain: %q — every enabled domain is a tell", method)
		}
	}

	if want := []string{"Target.getTargets", "Target.attachToTarget"}; !slices.Equal(f.sent(), want) {
		t.Errorf("attach sent %v, want exactly %v", f.sent(), want)
	}
}

// TestCommandsAfterAttachCarryTheSession checks the flattened session is used,
// so commands reach the page rather than the browser — and that still no domain
// gets enabled along the way.
func TestCommandsAfterAttachCarryTheSession(t *testing.T) {
	t.Parallel()

	f := newFakeBrowser(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	client, err := cdp.Dial(ctx, f.wsURL())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	if err := client.AttachToPage(ctx); err != nil {
		t.Fatalf("AttachToPage: %v", err)
	}
	if _, err := client.Send(ctx, "Emulation.setTimezoneOverride", map[string]any{"timezoneId": "Europe/Berlin"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	sent := f.sent()
	if !slices.Contains(sent, "Emulation.setTimezoneOverride") {
		t.Errorf("override never reached the browser; sent %v", sent)
	}
	for _, m := range sent {
		if strings.HasSuffix(m, ".enable") {
			t.Errorf("unexpected domain enable: %q", m)
		}
	}
}

// TestSendSurfacesProtocolErrors keeps a rejected command from looking like a
// success, which would leave a profile launched with an override missing.
func TestSendSurfacesProtocolErrors(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()

		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var req struct {
				ID int64 `json:"id"`
			}
			_ = json.Unmarshal(data, &req)
			msg := `{"id":` + itoa(req.ID) + `,"error":{"code":-32000,"message":"no such target"}}`
			if err := conn.Write(r.Context(), websocket.MessageText, []byte(msg)); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	client, err := cdp.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = client.Close() }()

	if _, err := client.Send(ctx, "Emulation.setTimezoneOverride", nil); err == nil {
		t.Fatal("a protocol error must surface, not pass as success")
	}
}
