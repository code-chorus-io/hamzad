// Package store persists browser profiles on disk and shares them through git.
// The store directory is a git repository: profiles.toml holds the non-secret
// configuration, secrets/<name>.age the age-encrypted proxy, and
// data/<name>.tar.age the age-encrypted session bundle. The unencrypted working
// data/<name> directory is gitignored and stays local.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/BurntSushi/toml"

	"github.com/code-chorus-io/hamzad/internal/domain/profile"
)

// ErrNotFound is returned when a named profile is absent from the store.
var ErrNotFound = errors.New("profile not found")

// ErrExists is returned when adding a profile whose name is already taken.
var ErrExists = errors.New("profile already exists")

const (
	profilesFile = "profiles.toml"
	dataDir      = "data"
	// The unencrypted working browser data stays local; the encrypted
	// data/<name>.tar.age bundles are shared, so only subdirectories are ignored.
	// The half-written temp files of an interrupted save are excluded too, since
	// Sync stages the whole store and would otherwise be able to commit one.
	gitignoreBody = "# unencrypted working browser data stays local (encrypted bundles are shared)\n" +
		"/data/*/\n" +
		"/.profiles-*.toml\n" +
		"/data/.*.tmp\n" +
		"/secrets/.*.tmp\n"
)

// Store is a handle to an on-disk profile store rooted at Dir.
//
// The profiles.toml mutations (Add, Put, Remove) are read-modify-write and are
// serialized by mu so a long-running process (e.g. the TUI) can mutate the
// store from several goroutines without losing updates. The one-shot CLI only
// ever holds a single Store and never contends, so mu is uncontended there.
type Store struct {
	Dir string

	mu sync.Mutex
}

// New returns a store rooted at dir. It does not touch the filesystem; call
// Ensure to create the directory layout.
func New(dir string) *Store {
	return &Store{Dir: dir}
}

// Ensure creates the store directory and its .gitignore if they are missing.
func (s *Store) Ensure() error {
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return fmt.Errorf("creating store dir %q: %w", s.Dir, err)
	}

	gi := filepath.Join(s.Dir, ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(gi, []byte(gitignoreBody), 0o600); err != nil {
			return fmt.Errorf("writing .gitignore: %w", err)
		}
	}

	return nil
}

// UserDataDir returns the isolated Chrome user-data directory for a profile.
func (s *Store) UserDataDir(name string) string {
	return filepath.Join(s.Dir, dataDir, name)
}

type profilesDoc struct {
	Profiles map[string]profile.Profile `toml:"profiles"`
}

func (s *Store) path() string {
	return filepath.Join(s.Dir, profilesFile)
}

// List returns all stored profiles sorted by name.
func (s *Store) List() ([]profile.Profile, error) {
	doc, err := s.load()
	if err != nil {
		return nil, err
	}

	out := make([]profile.Profile, 0, len(doc.Profiles))
	for name, p := range doc.Profiles {
		p.Name = name
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// Get returns the named profile or ErrNotFound.
func (s *Store) Get(name string) (profile.Profile, error) {
	doc, err := s.load()
	if err != nil {
		return profile.Profile{}, err
	}

	p, ok := doc.Profiles[name]
	if !ok {
		return profile.Profile{}, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	p.Name = name

	return p, nil
}

// Add stores a new profile, failing with ErrExists if the name is taken.
func (s *Store) Add(p profile.Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := doc.Profiles[p.Name]; ok {
		return fmt.Errorf("%w: %q", ErrExists, p.Name)
	}
	doc.Profiles[p.Name] = p

	return s.save(doc)
}

// Put inserts or replaces a profile after validating it.
func (s *Store) Put(p profile.Profile) error {
	if err := p.Validate(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return err
	}
	doc.Profiles[p.Name] = p

	return s.save(doc)
}

// Remove deletes the named profile and, when purgeData is set, its browsing
// data directory. It returns ErrNotFound when the profile is absent.
func (s *Store) Remove(name string, purgeData bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return err
	}
	if _, ok := doc.Profiles[name]; !ok {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	delete(doc.Profiles, name)

	if err := s.save(doc); err != nil {
		return err
	}

	// The encrypted secret and session bundle are shared artifacts of the
	// profile, so they go with it.
	if err := s.removeSecret(name); err != nil {
		return err
	}
	if err := s.removeBundle(name); err != nil {
		return err
	}

	if purgeData {
		if err := os.RemoveAll(s.UserDataDir(name)); err != nil {
			return fmt.Errorf("removing data dir for %q: %w", name, err)
		}
	}

	return nil
}

func (s *Store) load() (profilesDoc, error) {
	doc := profilesDoc{Profiles: map[string]profile.Profile{}}

	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return doc, fmt.Errorf("reading %s: %w", profilesFile, err)
	}

	if err := toml.Unmarshal(data, &doc); err != nil {
		return doc, fmt.Errorf("parsing %s: %w", profilesFile, err)
	}
	if doc.Profiles == nil {
		doc.Profiles = map[string]profile.Profile{}
	}

	return doc, nil
}

func (s *Store) save(doc profilesDoc) error {
	if err := s.Ensure(); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(s.Dir, ".profiles-*.toml")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()

	if err := toml.NewEncoder(tmp).Encode(doc); err != nil {
		_ = tmp.Close()

		return fmt.Errorf("encoding profiles: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	if err := os.Rename(tmp.Name(), s.path()); err != nil {
		return fmt.Errorf("replacing %s: %w", profilesFile, err)
	}

	return nil
}
