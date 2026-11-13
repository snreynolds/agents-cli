// Package profile stores profiles on disk: one folder per profile under
// <home>/profiles, holding profile.toml and the harness's config folder.
package profile

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/snreynolds/agents-cli/internal/atomicfile"
	"github.com/snreynolds/agents-cli/internal/filelock"
)

// Profile is a profile's settings, stored in profile.toml.
type Profile struct {
	Name               string   `toml:"-"` // the folder's name, not stored in the file
	Harness            string   `toml:"harness"`
	AllowedCredentials []string `toml:"allowed_credentials"` // credential IDs; ["*"] = all, including ones added later
}

// AllowsCredential reports whether the profile may receive the credential
// with this ID. It doesn't consider whether it's saved or whether the
// harness supports it.
func (p Profile) AllowsCredential(id string) bool {
	return slices.Contains(p.AllowedCredentials, "*") || slices.Contains(p.AllowedCredentials, id)
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidateName reports whether name can be used as a profile name.
func ValidateName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid profile name %q: use lowercase letters, digits, - and _ (max 64)", name)
	}
	return nil
}

// profileFolders are created inside every profile's folder.
var profileFolders = []string{
	"config", // the harness's config folder (its ConfigEnv points here)
	"run",    // one record per live run
}

// ErrNotFound is returned for a profile that doesn't exist.
var ErrNotFound = errors.New("profile not found")

// Store reads and writes profiles under a root folder (<home>/profiles).
type Store struct {
	root string
}

// NewStore returns a store rooted at root.
func NewStore(root string) *Store { return &Store{root: root} }

// Every Store method that touches disk gets its path from checkedDir, so no
// name can reach a path outside the profiles folder, whoever the caller is.
// Store protects only that and its own files (no overwriting, atomic
// writes); rules about what users may do (e.g. not removing a running
// profile) belong to its callers.

// checkedDir returns the profile's folder, refusing names that aren't safe to
// use in a path (e.g. "../x"). It doesn't check that the folder exists.
func (s *Store) checkedDir(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(s.root, name), nil
}

// ConfigDir is the folder the harness's ConfigEnv points at. It only builds
// the path: call it with a name that Create or Load has accepted.
func (s *Store) ConfigDir(name string) string { return filepath.Join(s.root, name, "config") }

// Create makes the profile's folders (mode 0700) and writes profile.toml.
// It fails if the profile already exists, and leaves nothing behind on error.
func (s *Store) Create(p Profile) (err error) {
	dir, err := s.checkedDir(p.Name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("profile %q already exists", p.Name)
		}
		return err
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()
	for _, sub := range profileFolders {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	data, err := toml.Marshal(p)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, "profile.toml"), data, 0o600)
}

// Load reads one profile. It fails with ErrNotFound if there's no such
// profile.
func (s *Store) Load(name string) (Profile, error) {
	dir, err := s.checkedDir(name)
	if err != nil {
		return Profile{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "profile.toml"))
	if errors.Is(err, os.ErrNotExist) {
		return Profile{}, fmt.Errorf("%w: %s (see agents ls)", ErrNotFound, name)
	}
	if err != nil {
		return Profile{}, err
	}
	// Strict, so a setting this version doesn't support (e.g. a sandbox
	// default from an older build) makes the profile unreadable instead of
	// being silently ignored.
	var p Profile
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", name, err)
	}
	p.Name = name
	return p, nil
}

// List returns every profile, sorted by name, plus the names of profile
// folders whose profile.toml is missing or can't be read.
func (s *Store) List() (profiles []Profile, unreadable []string, err error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return profiles, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || ValidateName(e.Name()) != nil {
			continue
		}
		if p, err := s.Load(e.Name()); err == nil {
			profiles = append(profiles, p)
		} else {
			unreadable = append(unreadable, e.Name())
		}
	}
	slices.SortFunc(profiles, func(a, b Profile) int { return strings.Compare(a.Name, b.Name) })
	return profiles, unreadable, nil
}

// Exists reports whether a profile folder with this name exists, even if its
// profile.toml is broken, so a broken profile can still be removed.
func (s *Store) Exists(name string) bool {
	dir, err := s.checkedDir(name)
	if err != nil {
		return false
	}
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// Remove deletes the profile's folder and everything in it. It works even if
// profile.toml is broken. A profile that doesn't exist is not an error:
// callers decide whether that matters.
func (s *Store) Remove(name string) error {
	dir, err := s.checkedDir(name)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// Lock takes the profile's lock, waiting while someone else holds it, and
// returns a function that releases it. Actions that check run records and
// then act on them hold it, so `rm` can't delete a profile between a run
// starting and its record being written.
//
// It fails with ErrNotFound if the profile doesn't exist, including if it was
// removed while waiting.
func (s *Store) Lock(name string) (unlock func(), err error) {
	dir, err := s.checkedDir(name)
	if err != nil {
		return nil, err
	}
	unlock, err = filelock.Lock(filepath.Join(dir, "lock"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	if err != nil {
		return nil, err
	}
	if !s.Exists(name) { // removed while we waited
		unlock()
		return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return unlock, nil
}
