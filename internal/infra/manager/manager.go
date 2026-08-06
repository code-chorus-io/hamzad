// Package manager supervises multiple concurrent browser sessions inside a
// single long-running process (the TUI). Each session wraps a blocking
// chrome.Launch call in its own goroutine, keyed by profile name and tied to a
// cancellable context so the UI can close a browser by cancelling it. State
// changes are pushed to an observer via OnEvent, which the TUI wires to the
// Bubble Tea program so it never touches manager state from another goroutine.
package manager

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	domain "github.com/1995parham/koochooloologin/internal/domain/profile"
	"github.com/1995parham/koochooloologin/internal/infra/chrome"
	"github.com/1995parham/koochooloologin/internal/infra/crypt"
	"github.com/1995parham/koochooloologin/internal/infra/geo"
	"github.com/1995parham/koochooloologin/internal/infra/store"
)

// ErrAlreadyRunning is returned by Open when a profile already has a live
// session. Two launches of one profile would share a single Chrome user-data
// directory, which Chrome locks, so this is refused up front.
var ErrAlreadyRunning = errors.New("a session for this profile is already running")

// Status is the lifecycle state of a session.
type Status int

const (
	// StatusStarting means the browser is being prepared (bundle restore,
	// option build) but the window is not up yet.
	StatusStarting Status = iota
	// StatusRunning means chrome.Launch is active and the window is open.
	StatusRunning
	// StatusClosed means the session ended cleanly (window closed or cancelled).
	StatusClosed
	// StatusError means the session ended because chrome.Launch failed.
	StatusError
)

// String renders a status for the session table.
func (s Status) String() string {
	switch s {
	case StatusStarting:
		return "starting"
	case StatusRunning:
		return "running"
	case StatusClosed:
		return "closed"
	case StatusError:
		return "error"
	default:
		return "unknown"
	}
}

// Active reports whether the session occupies its profile's data directory and
// so blocks a second launch.
func (s Status) Active() bool {
	return s == StatusStarting || s == StatusRunning
}

// Session is an immutable snapshot of one session's state, safe to hand to the
// UI goroutine.
type Session struct {
	Name      string
	Status    Status
	StartedAt time.Time
	DebugPort int
	// Note carries non-fatal heads-up text about how the session launched — the
	// overrides a clean launch dropped, or a shared bundle left unrestored. The
	// CLI prints the same warnings to stdout; the TUI has no stdout to print to,
	// so they ride along on the session instead.
	Note string
	Err  error
}

// OpenOpts tunes a single Open call, mirroring the flags of `profile open`.
type OpenOpts struct {
	StartURL  string // overrides the profile's start URL when non-empty
	DebugPort int    // fixed remote-debugging port, or 0 for none
	AutoGeo   bool   // derive timezone & geolocation from the proxy exit IP
	NoRestore bool   // skip decrypting the shared session bundle before launch
	NoSave    bool   // skip re-encrypting the bundle after the window closes
}

// session is the manager's private, mutable per-profile state.
type session struct {
	name      string
	status    Status
	startedAt time.Time
	debugPort int
	note      string
	err       error
	cancel    context.CancelFunc
}

func (s *session) snapshot() Session {
	return Session{
		Name:      s.name,
		Status:    s.status,
		StartedAt: s.startedAt,
		DebugPort: s.debugPort,
		Note:      s.note,
		Err:       s.err,
	}
}

// Manager owns the set of live sessions and the store/crypt handles they need.
type Manager struct {
	store    *store.Store
	crypt    crypt.Crypt
	execPath string

	mu       sync.Mutex
	sessions map[string]*session
	notify   func(Session)
	wg       sync.WaitGroup
}

// New builds a manager over the given store and crypt. execPath is the resolved
// Chrome binary; when empty the launcher auto-detects one.
func New(st *store.Store, c crypt.Crypt, execPath string) *Manager {
	return &Manager{
		store:    st,
		crypt:    c,
		execPath: execPath,
		sessions: map[string]*session{},
	}
}

// OnEvent registers the observer invoked (from a background goroutine) whenever
// a session's state changes. It is called with an immutable snapshot.
func (m *Manager) OnEvent(fn func(Session)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notify = fn
}

// emit reads a snapshot and the observer under the lock, then fires the
// observer outside it so callbacks (e.g. program.Send) never deadlock on mu.
func (m *Manager) emit(name string) {
	m.mu.Lock()
	s, ok := m.sessions[name]
	var (
		snap   Session
		notify func(Session)
	)
	if ok {
		snap = s.snapshot()
		notify = m.notify
	}
	m.mu.Unlock()

	if ok && notify != nil {
		notify(snap)
	}
}

// Get returns a snapshot of the named session, if one exists.
func (m *Manager) Get(name string) (Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[name]
	if !ok {
		return Session{}, false
	}

	return s.snapshot(), true
}

// List returns snapshots of all known sessions, sorted by profile name.
func (m *Manager) List() []Session {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.snapshot())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out
}

// Open launches a browser for the named profile and tracks it. It restores the
// session bundle and builds the launch options synchronously (so callers should
// run it off the UI goroutine, inside a tea.Cmd), then hands the blocking
// chrome.Launch to a background goroutine. It returns ErrAlreadyRunning if the
// profile already has a live session. The launched browser deliberately
// outlives ctx (a short-lived Cmd context); its lifetime is governed by the
// cancel func stored on the session, not by ctx.
//
//nolint:contextcheck // detaching the session from the caller ctx is intentional.
func (m *Manager) Open(ctx context.Context, name string, o OpenOpts) error {
	m.mu.Lock()
	if s, ok := m.sessions[name]; ok && s.status.Active() {
		m.mu.Unlock()

		return fmt.Errorf("%w: %q", ErrAlreadyRunning, name)
	}
	s := &session{name: name, status: StatusStarting, startedAt: time.Now()}
	m.sessions[name] = s
	m.mu.Unlock()
	m.emit(name)

	keptLocal, err := m.restore(name, o.NoRestore)
	if err != nil {
		m.fail(name, err)

		return err
	}

	opts, err := m.buildOptions(ctx, name, o)
	if err != nil {
		m.fail(name, err)

		return err
	}

	// The session outlives the caller's ctx (which may be a short-lived Cmd
	// context); its lifetime is governed by cancel, invoked from Close/CloseAll.
	launchCtx, cancel := context.WithCancel(context.Background())

	m.mu.Lock()
	s.cancel = cancel
	s.status = StatusRunning
	s.debugPort = o.DebugPort
	s.note = launchNotes(keptLocal, o.DebugPort, opts)
	m.mu.Unlock()
	m.emit(name)

	m.wg.Go(func() {
		defer cancel()

		launchErr := chrome.Launch(launchCtx, opts)

		if !o.NoSave {
			m.save(name)
		}

		m.mu.Lock()
		// A launch error with the context still live is a genuine failure; if
		// the context was cancelled the browser was closed on purpose.
		if launchErr != nil && launchCtx.Err() == nil {
			s.status = StatusError
			s.err = launchErr
		} else {
			s.status = StatusClosed
		}
		s.cancel = nil
		m.mu.Unlock()
		m.emit(name)
	})

	return nil
}

// launchNotes assembles the non-fatal heads-up text for a session the UI has no
// other channel to hear about: a shared bundle left unrestored, and the
// overrides a no-CDP launch cannot apply. The latter matters most here, since
// the TUI's operating-system preset pairs a platform with a user-agent and a
// clean launch applies only the user-agent.
func launchNotes(keptLocal bool, debugPort int, opts chrome.Options) string {
	var notes []string

	if keptLocal {
		notes = append(notes, "kept local session, shared bundle not restored")
	}
	if debugPort == 0 {
		if dropped := chrome.CleanModeDropped(opts); dropped != "" {
			notes = append(notes, dropped+" not applied without a CDP port")
		}
	}

	return strings.Join(notes, "; ")
}

// Close cancels the named session's browser, if it is live. The session's
// goroutine handles the bundle save and the final state transition.
func (m *Manager) Close(name string) {
	m.mu.Lock()
	s, ok := m.sessions[name]
	var cancel context.CancelFunc
	if ok {
		cancel = s.cancel
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
}

// CloseAll cancels every live session and waits for their goroutines to finish
// (saving bundles and releasing browsers). Call it on shutdown.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(m.sessions))
	for _, s := range m.sessions {
		if s.cancel != nil {
			cancels = append(cancels, s.cancel)
		}
	}
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	m.wg.Wait()
}

// fail marks a session errored before it ever reached the running state.
func (m *Manager) fail(name string, err error) {
	m.mu.Lock()
	if s, ok := m.sessions[name]; ok {
		s.status = StatusError
		s.err = err
		s.cancel = nil
	}
	m.mu.Unlock()
	m.emit(name)
}

// restore decrypts the shared session bundle over the local working data when a
// bundle exists and no working copy is present yet, mirroring `profile open`'s
// restoreSession. It reports whether a bundle was deliberately left unrestored
// because local data won, so the caller can say so rather than let it pass
// unnoticed.
func (m *Manager) restore(name string, disabled bool) (bool, error) {
	if disabled || !m.store.HasBundle(name) {
		return false, nil
	}
	if dirExists(m.store.UserDataDir(name)) {
		return true, nil
	}

	if err := m.store.UnpackBundle(m.crypt, name); err != nil {
		return false, fmt.Errorf("restoring session for %q: %w", name, err)
	}

	return false, nil
}

// save re-encrypts the working data into the shared bundle when encryption is
// configured. Errors are swallowed: a save failure must not crash the manager,
// and the working data is still on disk for a later `profile push`.
func (m *Manager) save(name string) {
	if !m.crypt.Configured() {
		return
	}
	_ = m.store.PackBundle(m.crypt, name)
}

// buildOptions resolves a profile into chrome launch options, decrypting its
// proxy and applying the start-url override and optional auto-geo, mirroring
// `profile open`'s openOptions.
func (m *Manager) buildOptions(ctx context.Context, name string, o OpenOpts) (chrome.Options, error) {
	p, err := m.store.Get(name)
	if err != nil {
		return chrome.Options{}, err
	}

	opts := chrome.Options{
		ExecPath:    m.execPath,
		UserDataDir: m.store.UserDataDir(name),
		Timezone:    p.Timezone,
		StartURL:    p.StartURL,
		DebugPort:   o.DebugPort,
		Fingerprint: p.Fingerprint,
	}
	if o.StartURL != "" {
		opts.StartURL = o.StartURL
	}

	proxyURL, err := m.resolveProxy(name)
	if err != nil {
		return chrome.Options{}, err
	}
	opts.Proxy = proxyURL

	if o.AutoGeo {
		if err := applyAutoGeo(ctx, &opts, proxyURL); err != nil {
			return chrome.Options{}, err
		}
	}

	return opts, nil
}

// resolveProxy decrypts and parses a profile's proxy, or returns nil when the
// profile has none.
func (m *Manager) resolveProxy(name string) (*url.URL, error) {
	if !m.store.HasSecret(name) {
		return nil, nil //nolint:nilnil // nil URL, nil error means "no proxy"
	}

	raw, err := m.store.Proxy(m.crypt, name)
	if err != nil {
		return nil, err
	}

	return domain.ParseProxy(raw)
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.IsDir()
}

// applyAutoGeo fills the session timezone and geolocation from the proxy's exit
// IP, leaving any values the profile already pins untouched.
func applyAutoGeo(ctx context.Context, opts *chrome.Options, proxyURL *url.URL) error {
	loc, err := geo.Lookup(ctx, proxyURL)
	if err != nil {
		return err
	}

	if opts.Timezone == "" {
		opts.Timezone = loc.Timezone
	}
	if opts.Fingerprint.Geolocation == nil {
		opts.Fingerprint.Geolocation = &domain.LatLng{Latitude: loc.Latitude, Longitude: loc.Longitude}
	}

	return nil
}
