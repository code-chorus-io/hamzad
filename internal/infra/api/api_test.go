package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/api"
	"github.com/code-chorus-io/hamzad/internal/infra/manager"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

const (
	token = "s3cret"
	work  = "work"
)

// fakeSessions stands in for the manager: Open marks a session running, Close
// marks it closed after a short delay, as a real browser's save would.
type fakeSessions struct {
	mu       sync.Mutex
	sessions map[string]manager.Session
	opened   manager.OpenOpts
}

func (f *fakeSessions) Open(_ context.Context, name string, o manager.OpenOpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if s, ok := f.sessions[name]; ok && s.Status.Active() {
		return manager.ErrAlreadyRunning
	}
	f.opened = o
	f.sessions[name] = manager.Session{Name: name, Status: manager.StatusRunning, DebugPort: o.DebugPort}

	return nil
}

func (f *fakeSessions) Close(name string) {
	go func() {
		time.Sleep(50 * time.Millisecond)
		f.mu.Lock()
		defer f.mu.Unlock()
		s := f.sessions[name]
		s.Status = manager.StatusClosed
		f.sessions[name] = s
	}()
}

func (f *fakeSessions) Get(name string) (manager.Session, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[name]

	return s, ok
}

func (f *fakeSessions) List() []manager.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]manager.Session, 0, len(f.sessions))
	for _, s := range f.sessions {
		out = append(out, s)
	}

	return out
}

type fakeProfiles map[string]domain.Profile

func (f fakeProfiles) List() ([]domain.Profile, error) {
	out := make([]domain.Profile, 0, len(f))
	for _, p := range f {
		out = append(out, p)
	}

	return out, nil
}

func (f fakeProfiles) Get(name string) (domain.Profile, error) {
	p, ok := f[name]
	if !ok {
		return domain.Profile{}, fmt.Errorf("%w: %q", store.ErrNotFound, name)
	}

	return p, nil
}

func (f fakeProfiles) HasSecret(name string) bool { return name == work }

func newServer(t *testing.T) (*httptest.Server, *fakeSessions) {
	t.Helper()

	sessions := &fakeSessions{sessions: map[string]manager.Session{}}
	h := api.New(api.Options{
		Sessions: sessions,
		Profiles: fakeProfiles{
			work: {Name: work, Proxy: "socks5://user:pass@host:1080"},
			"home": {Name: "home"},
		},
		Token:    token,
		FreePort: func() (int, error) { return 45678, nil },
		WaitEndpoint: func(_ context.Context, port int) (string, error) {
			return fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/abc", port), nil
		},
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return srv, sessions
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, mutate func(*http.Request)) (int, map[string]any) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if mutate != nil {
		mutate(req)
	}

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var out any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	obj, _ := out.(map[string]any)

	return resp.StatusCode, obj
}

func TestRejectsMissingToken(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)
	code, _ := call(t, srv, http.MethodGet, "/v1/profiles", "", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong")
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestRejectsForeignHost(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)
	// What a DNS-rebinding page sends: the right port, its own hostname.
	code, _ := call(t, srv, http.MethodGet, "/v1/profiles", "", func(r *http.Request) {
		r.Host = "evil.example.com:36912"
	})
	if code != http.StatusMisdirectedRequest {
		t.Fatalf("status = %d, want 421", code)
	}

	code, _ = call(t, srv, http.MethodGet, "/v1/profiles", "", func(r *http.Request) {
		r.Host = "localhost:36912"
	})
	if code != http.StatusOK {
		t.Fatalf("localhost status = %d, want 200", code)
	}
}

func TestProfilesHideTheProxy(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)
	code, got := call(t, srv, http.MethodGet, "/v1/profiles/work", "", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got["proxy"] != true {
		t.Fatalf("proxy = %v, want true", got["proxy"])
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "pass") {
		t.Fatalf("proxy credentials leaked: %s", raw)
	}
}

func TestStartReturnsEndpointAndConflicts(t *testing.T) {
	t.Parallel()

	srv, sessions := newServer(t)

	code, got := call(t, srv, http.MethodPost, "/v1/profiles/work/start", `{"start_url":"https://example.com"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d: %v", code, got)
	}
	if got["ws_endpoint"] != "ws://127.0.0.1:45678/devtools/browser/abc" || got["cdp_port"] != float64(45678) {
		t.Fatalf("got %v", got)
	}
	if sessions.opened.StartURL != "https://example.com" || sessions.opened.DebugPort != 45678 {
		t.Fatalf("opened with %+v", sessions.opened)
	}

	code, got = call(t, srv, http.MethodPost, "/v1/profiles/work/start", "", nil)
	if code != http.StatusConflict || got["ws_endpoint"] == nil {
		t.Fatalf("second start: status = %d, body %v; want 409 with the running session", code, got)
	}
}

func TestStartRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)
	code, _ := call(t, srv, http.MethodPost, "/v1/profiles/work/start", `{"startUrl":"x"}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", code)
	}
}

func TestStopWaitsForTheSave(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)
	if code, _ := call(t, srv, http.MethodPost, "/v1/profiles/home/stop", "", nil); code != http.StatusConflict {
		t.Fatalf("stop of an idle profile: status = %d, want 409", code)
	}

	call(t, srv, http.MethodPost, "/v1/profiles/home/start", "", nil)
	code, got := call(t, srv, http.MethodPost, "/v1/profiles/home/stop", "", nil)
	if code != http.StatusOK || got["status"] != "closed" {
		t.Fatalf("stop: status = %d, body %v", code, got)
	}
}

func TestUnknownProfile(t *testing.T) {
	t.Parallel()

	srv, _ := newServer(t)
	if code, _ := call(t, srv, http.MethodPost, "/v1/profiles/nope/start", "", nil); code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", code)
	}
}
