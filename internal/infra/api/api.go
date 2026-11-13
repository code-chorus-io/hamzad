// Package api is hamzad's local automation API: start and stop profiles over
// HTTP and get back the DevTools endpoint Puppeteer, Playwright or Selenium
// attach to — the role GoLogin's local API plays for its SDKs.
//
// It is a control channel for real, signed-in browsers, so it is locked down:
// every request needs the bearer token, and the Host header must name a
// loopback address. The second check is what stops DNS rebinding — a web page
// that points its own hostname at 127.0.0.1 can reach the port, but its
// requests carry its hostname, not ours.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	domain "github.com/code-chorus-io/hamzad/internal/domain/profile"
	"github.com/code-chorus-io/hamzad/internal/infra/manager"
	"github.com/code-chorus-io/hamzad/internal/infra/store"
)

// stopTimeout bounds the wait for a stopped browser to quit and its session to
// be saved, which includes the cookie export.
const stopTimeout = 90 * time.Second

// endpointTimeout bounds the wait for a started browser's DevTools endpoint.
const endpointTimeout = 30 * time.Second

// maxBody caps a request body; the only bodies are small option objects.
const maxBody = 1 << 16

// Sessions is the part of manager.Manager the API drives.
type Sessions interface {
	Open(ctx context.Context, name string, o manager.OpenOpts) error
	Close(name string)
	Get(name string) (manager.Session, bool)
	List() []manager.Session
}

// Profiles is the part of store.Store the API reads.
type Profiles interface {
	List() ([]domain.Profile, error)
	Get(name string) (domain.Profile, error)
	HasSecret(name string) bool
}

// Options configures the handler.
type Options struct {
	Sessions Sessions
	Profiles Profiles
	// Token is the bearer token every request must carry.
	Token string
	// FreePort picks a debugging port for a session that did not ask for one.
	FreePort func() (int, error)
	// WaitEndpoint waits for a browser's DevTools endpoint on a port.
	WaitEndpoint func(ctx context.Context, port int) (string, error)
}

type server struct {
	Options
}

// New returns the API handler.
func New(o Options) http.Handler {
	s := &server{Options: o}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/profiles", s.listProfiles)
	mux.HandleFunc("GET /v1/profiles/{name}", s.getProfile)
	mux.HandleFunc("POST /v1/profiles/{name}/start", s.start)
	mux.HandleFunc("POST /v1/profiles/{name}/stop", s.stop)
	mux.HandleFunc("GET /v1/sessions", s.listSessions)

	return s.guard(mux)
}

// guard enforces the loopback Host and the bearer token on every request.
func (s *server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeError(w, http.StatusMisdirectedRequest, "host must be a loopback address")

			return
		}

		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(s.Token)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="hamzad"`)
			writeError(w, http.StatusUnauthorized, "missing or wrong bearer token")

			return
		}

		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether a Host header names this machine by address or
// as localhost.
func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")

	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// profileView is a profile as the API shows it. Secrets never leave: only
// whether a proxy is set.
type profileView struct {
	Name     string       `json:"name"`
	Notes    string       `json:"notes,omitempty"`
	Timezone string       `json:"timezone,omitempty"`
	Proxy    bool         `json:"proxy"`
	Session  *sessionView `json:"session,omitempty"`
}

// sessionView is a session as the API shows it.
type sessionView struct {
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at"`
	CDPPort   int       `json:"cdp_port,omitempty"`
	// WSEndpoint is the browser websocket URL: puppeteer.connect's
	// browserWSEndpoint, Playwright's chromium.connectOverCDP.
	WSEndpoint string `json:"ws_endpoint,omitempty"`
	// HTTPEndpoint is the DevTools HTTP root, for clients that discover the
	// websocket themselves (Selenium's debuggerAddress takes host:port).
	HTTPEndpoint string `json:"http_endpoint,omitempty"`
	Note         string `json:"note,omitempty"`
	Error        string `json:"error,omitempty"`
}

func (s *server) view(ctx context.Context, sess manager.Session) *sessionView {
	v := &sessionView{
		Name:      sess.Name,
		Status:    sess.Status.String(),
		StartedAt: sess.StartedAt,
		Note:      sess.Note,
	}
	if sess.Err != nil {
		v.Error = sess.Err.Error()
	}
	if sess.Status == manager.StatusRunning && sess.DebugPort > 0 {
		v.CDPPort = sess.DebugPort
		v.HTTPEndpoint = fmt.Sprintf("http://127.0.0.1:%d", sess.DebugPort)
		// Ask the browser rather than remember: the URL carries an id that is
		// only valid while that browser runs.
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if ws, err := s.WaitEndpoint(ctx, sess.DebugPort); err == nil {
			v.WSEndpoint = ws
		}
	}

	return v
}

func (s *server) profileView(ctx context.Context, p domain.Profile) profileView {
	v := profileView{Name: p.Name, Notes: p.Notes, Timezone: p.Timezone, Proxy: s.Profiles.HasSecret(p.Name)}
	if sess, ok := s.Sessions.Get(p.Name); ok {
		v.Session = s.view(ctx, sess)
	}

	return v
}

func (s *server) listProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := s.Profiles.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	out := make([]profileView, 0, len(profiles))
	for _, p := range profiles {
		out = append(out, s.profileView(r.Context(), p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.profileView(r.Context(), p))
}

func (s *server) listSessions(w http.ResponseWriter, r *http.Request) {
	sessions := s.Sessions.List()
	out := make([]*sessionView, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, s.view(r.Context(), sess))
	}
	writeJSON(w, http.StatusOK, out)
}

// startRequest is the optional body of a start call; every field mirrors a
// `profile open` flag.
type startRequest struct {
	StartURL  string `json:"start_url"`
	AutoGeo   bool   `json:"auto_geo"`
	NoRestore bool   `json:"no_restore"`
	NoSave    bool   `json:"no_save"`
	CDPPort   int    `json:"cdp_port"`
}

func (s *server) start(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}

	var req startRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "bad request body: "+err.Error())

			return
		}
	}

	// Automation needs the DevTools port, so a session started here always
	// has one — the clean, port-less launch is what `profile open` is for.
	port := req.CDPPort
	if port == 0 {
		var err error
		if port, err = s.FreePort(); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())

			return
		}
	}

	err := s.Sessions.Open(r.Context(), p.Name, manager.OpenOpts{
		StartURL:  req.StartURL,
		DebugPort: port,
		AutoGeo:   req.AutoGeo,
		NoRestore: req.NoRestore,
		NoSave:    req.NoSave,
	})
	if errors.Is(err, manager.ErrAlreadyRunning) {
		sess, _ := s.Sessions.Get(p.Name)
		writeJSON(w, http.StatusConflict, s.view(r.Context(), sess))

		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), endpointTimeout)
	defer cancel()
	if _, err := s.WaitEndpoint(ctx, port); err != nil {
		sess, _ := s.Sessions.Get(p.Name)
		msg := "browser did not come up: " + err.Error()
		if sess.Err != nil {
			msg = "browser failed: " + sess.Err.Error()
		}
		writeError(w, http.StatusBadGateway, msg)

		return
	}

	sess, _ := s.Sessions.Get(p.Name)
	writeJSON(w, http.StatusOK, s.view(r.Context(), sess))
}

func (s *server) stop(w http.ResponseWriter, r *http.Request) {
	p, ok := s.lookup(w, r)
	if !ok {
		return
	}

	sess, ok := s.Sessions.Get(p.Name)
	if !ok || !sess.Status.Active() {
		writeError(w, http.StatusConflict, "profile is not running")

		return
	}

	s.Sessions.Close(p.Name)

	// Answer only once the session is saved, so a caller that stops and then
	// syncs or reopens never races the bundle write.
	ctx, cancel := context.WithTimeout(r.Context(), stopTimeout)
	defer cancel()
	for {
		sess, _ = s.Sessions.Get(p.Name)
		if !sess.Status.Active() {
			writeJSON(w, http.StatusOK, s.view(r.Context(), sess))

			return
		}

		select {
		case <-ctx.Done():
			writeError(w, http.StatusGatewayTimeout, "browser has not exited yet")

			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// lookup resolves the {name} path value, writing a 404 when there is no such
// profile.
func (s *server) lookup(w http.ResponseWriter, r *http.Request) (domain.Profile, bool) {
	p, err := s.Profiles.Get(r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())

		return domain.Profile{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())

		return domain.Profile{}, false
	}

	return p, true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The status line is already out; a failed body write has no one to tell.
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
