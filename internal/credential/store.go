package credential

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/snreynolds/agents-cli/internal/atomicfile"
	"github.com/snreynolds/agents-cli/internal/filelock"
)

// Entry is one saved credential.
type Entry struct {
	Secret string    `json:"secret"` // the key or token profiles receive
	Added  time.Time `json:"added"`

	// Set only for OAuth credentials, which expire and are renewed.
	// The refresh token never reaches a profile: profiles get Secret only.
	Refresh string    `json:"refresh,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
	Account string    `json:"account,omitempty"`
}

// String describes an entry without its secrets, so printing one by
// mistake (e.g. with %v in an error) can't leak them.
func (e Entry) String() string {
	return fmt.Sprintf("credential added %s (secrets hidden)", e.Added.Format(time.RFC3339))
}

// GoString is String for %#v.
func (e Entry) GoString() string { return e.String() }

// ErrNotFound is returned by Renew for a credential that isn't saved.
var ErrNotFound = errors.New("login not saved")

// Store reads and writes credentials.json in its folder.
type Store struct {
	dir string
}

// NewStore returns a store kept in dir (<home>/credentials). Nothing is
// created until the first save.
func NewStore(dir string) *Store { return &Store{dir: dir} }

func (s *Store) file() string { return filepath.Join(s.dir, "credentials.json") }

// Load returns every saved credential by ID. No file yet means none saved. It
// doesn't take the lock: saves replace the file in one step, so a reader
// sees either the old file or the new one.
func (s *Store) Load() (map[string]Entry, error) {
	data, err := os.ReadFile(s.file())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Entry{}, nil
	}
	if err != nil {
		return nil, err
	}
	entries := map[string]Entry{}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	// A file holding null decodes without error but leaves no map.
	if err := dec.Decode(&entries); err != nil || entries == nil {
		// Not the decoder's message: it can quote a value, i.e. a secret.
		return nil, fmt.Errorf("%s is damaged; fix it or delete it and log in again", s.file())
	}
	return entries, nil
}

// Save saves the credential c's sign-in gave, replacing any saved under the
// same ID, and reports whether it replaced one. Runs started from then on
// get it. It refuses an entry that doesn't fit c (see checkEntry).
func (s *Store) Save(c Spec, e Entry) (replaced bool, err error) {
	if err := checkEntry(c, e); err != nil {
		return false, err
	}
	e.Added = time.Now().UTC()
	return s.set(c.ID, e)
}

// checkEntry checks that e is what signing in to c gives: a plausible
// secret, and a refresh token and expiry if and only if c renews.
func checkEntry(c Spec, e Entry) error {
	hasRenewal := e.Refresh != "" || !e.Expires.IsZero() || e.Account != ""
	switch {
	case c.Renews() && (e.Refresh == "" || e.Expires.IsZero()):
		// Renewing needs the refresh token only a sign-in gives.
		return fmt.Errorf("%s can't be pasted; it signs in with your browser: agents auth login %s", c.ID, c.ID)
	case !c.Renews() && hasRenewal:
		return fmt.Errorf("%s is a key or token; it has no refresh token or expiry", c.ID)
	}
	return checkSecret(e.Secret)
}

// checkSecret rejects input that can't be a key or token: empty, or
// containing spaces, line breaks or control characters (e.g. a paste that
// caught extra text; a terminal prompt turns pasted line breaks into
// spaces). It never guesses at a provider's key format.
func checkSecret(secret string) error {
	if secret == "" {
		return errors.New("nothing was entered")
	}
	if strings.ContainsFunc(secret, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return errors.New("a key or token has no spaces or line breaks; check what was pasted")
	}
	return nil
}

// errNoRefresh is why a saved credential without a refresh token (e.g.
// edited by hand) can't be renewed. No request is made.
var errNoRefresh = errors.New("it has no refresh token")

// renewBefore is how close to expiring an OAuth credential is renewed: well
// before it stops working, so a session started now keeps working for days.
const renewBefore = 48 * time.Hour

// RenewDue reports whether e expires and is close enough to expiring to be
// renewed (see Store.Renew).
func (e Entry) RenewDue() bool {
	return !e.Expires.IsZero() && time.Until(e.Expires) <= renewBefore
}

// Renew renews the saved credential c if it's due (Entry.RenewDue), asking
// c's Renewer, and saves the result. It holds the store's lock throughout:
// a refresh token works once, so two agents commands must not both spend
// it. If another command renewed it meanwhile, that one is returned as is.
// It fails with ErrNotFound if c isn't saved, and without asking the
// provider if it has no refresh token.
func (s *Store) Renew(ctx context.Context, c Spec) (Entry, error) {
	r, ok := c.Source.(Renewer)
	if !ok {
		return Entry{}, fmt.Errorf("%s doesn't expire, so it isn't renewed", c.ID)
	}
	return s.updateEntry(c.ID, func(e Entry) (Entry, error) {
		switch {
		case !e.RenewDue():
			return e, nil // renewed by another agents command while this one waited
		case e.Refresh == "":
			return e, errNoRefresh
		}
		renewed, err := r.Renew(ctx, e)
		if err != nil {
			return e, err
		}
		renewed.Added = e.Added
		return renewed, nil
	})
}

// set saves e under id, replacing any saved there, and reports whether it
// replaced one. Callers check e first.
func (s *Store) set(id string, e Entry) (replaced bool, err error) {
	err = s.update(func(entries map[string]Entry) error {
		_, replaced = entries[id]
		entries[id] = e
		return nil
	})
	return replaced, err
}

// updateEntry changes a saved credential under the store's lock: change
// gets the saved entry and returns the one to save. If change fails,
// nothing is saved. It fails with ErrNotFound if the credential isn't
// saved.
func (s *Store) updateEntry(id string, change func(Entry) (Entry, error)) (Entry, error) {
	var saved Entry
	err := s.update(func(entries map[string]Entry) error {
		e, ok := entries[id]
		if !ok {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		e, err := change(e)
		if err != nil {
			return err
		}
		entries[id], saved = e, e
		return nil
	})
	return saved, err
}

// Remove deletes a saved credential and reports whether there was one.
func (s *Store) Remove(id string) (removed bool, err error) {
	err = s.update(func(entries map[string]Entry) error {
		_, removed = entries[id]
		delete(entries, id)
		return nil
	})
	return removed, err
}

// update changes the saved credentials under the store's lock, so two agents
// commands saving at once can't lose each other's change. If change fails,
// nothing is written.
func (s *Store) update(change func(map[string]Entry) error) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0o700); err != nil { // in case it already existed with looser permissions
		return err
	}
	unlock, err := filelock.Lock(filepath.Join(s.dir, "lock"))
	if err != nil {
		return err
	}
	defer unlock()
	// A crashed save's temp file holds copies of the secrets, so a revoked
	// credential must not survive in one.
	if err := atomicfile.RemoveLeftovers(s.file()); err != nil {
		return err
	}
	entries, err := s.Load()
	if err != nil {
		return err
	}
	if err := change(entries); err != nil {
		return err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.file(), append(data, '\n'), 0o600)
}
