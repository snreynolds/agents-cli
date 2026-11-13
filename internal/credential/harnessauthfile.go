package credential

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/snreynolds/agents-cli/internal/atomicfile"
)

// Most harnesses read a credential from an env var, which app sets when it
// starts them. A few read one only from their own auth file: pi reads a
// ChatGPT subscription only from its auth.json. Provisioning writes the
// credential into that file before a run, and unprovisioning removes it
// again on revoke. This file holds every auth file format agents can
// write.

// HarnessAuthFile is one place a credential is provisioned: an entry in a login
// file a harness reads, in a profile's config folder. app builds it from
// the harness's binding.
type HarnessAuthFile struct {
	Dir    string // the profile's config folder; must be a real folder, not a symlink
	File   string // a file name in Dir, e.g. "auth.json"
	Format string // the file's layout, one of Formats()
	Key    string // the credential's entry in the file, e.g. "openai-codex"
}

// piAuthJSON is pi's auth.json: one JSON object, keyed by provider, written
// with mode 0600 and locked as the proper-lockfile npm package does (pi
// 0.87.1 pi-coding-agent/dist/core/auth-storage.js). pi notices a rewritten
// file at its next request: it compares the file's inode, size and times
// before each read (getFileRevision in utils/paths.js).
const piAuthJSON = "pi-auth-json"

// Formats returns the names of the auth file formats agents can write,
// for harness validation. There's one; a second would make Provision and
// Unprovision choose a writer by format.
func Formats() []string {
	return []string{piAuthJSON}
}

// managedRefresh is written in place of a credential's real refresh token.
// Only agents' store holds the real one, so profiles can't spend it (each
// refresh returns a new one and the old one stops working) and a profile's
// files never hold a long-lived secret. It also marks the entry as agents':
// one with any other refresh token is the user's own login, made inside the
// harness, and agents never replaces or removes it.
const managedRefresh = "agents-managed"

// errNotRegular means a auth file is a symlink, folder, pipe or device:
// agents never reads or writes through one.
var errNotRegular = errors.New("isn't a regular file; agents won't read or write through it")

// Provision writes e into f: its access token, when that expires, and its
// account (never the refresh token; see managedRefresh). It replaces any
// copy agents wrote there before and keeps everything else in the file. It
// leaves the file alone, returning written = false, if it holds the user's
// own login for this key, or if it's a file agents can't have written (a
// symlink, e.g. to the user's main login, or another non-regular file).
func Provision(f HarnessAuthFile, e Entry) (written bool, err error) {
	path, err := f.resolve()
	if err != nil {
		return false, err
	}
	written, err = writePiCredential(path, f.Key, e)
	if errors.Is(err, errNotRegular) {
		return false, nil
	}
	return written, err
}

// Unprovision removes the credential from f if agents wrote it, and
// reports whether it did. The user's own login, a missing file or folder,
// and a file agents can't have written are left alone.
func Unprovision(f HarnessAuthFile) (removed bool, err error) {
	path, err := f.resolve()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // no config folder, so no auth file
	}
	if err != nil {
		return false, err
	}
	removed, err = removePiCredential(path, f.Key)
	if errors.Is(err, errNotRegular) {
		return false, nil
	}
	return removed, err
}

// resolve returns f's path after checking f is safe to use. The
// folder is inside a profile, which the harness (and, later, a sandbox) can
// write to, so it must be a real folder (a linked one would point agents'
// writes at another folder, e.g. the user's main ~/.pi/agent) and File a
// plain name in it.
func (f HarnessAuthFile) resolve() (string, error) {
	if f.Format != piAuthJSON {
		return "", fmt.Errorf("unknown auth file format %q", f.Format)
	}
	if f.File == "" || f.File != filepath.Base(f.File) || f.File == "." || f.File == ".." || f.Key == "" {
		return "", fmt.Errorf("invalid auth file %q (key %q)", f.File, f.Key)
	}
	info, err := os.Lstat(f.Dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s isn't a folder (or is a symlink); agents won't write logins through it", f.Dir)
	}
	return filepath.Join(f.Dir, f.File), nil
}

// writePiCredential puts a credential agents manages under key in pi's
// auth.json, unless the user's own login is there.
func writePiCredential(path, key string, e Entry) (written bool, err error) {
	return changePiAuth(path, func(entries map[string]json.RawMessage) (bool, error) {
		if piOwner(entries[key]) == ownerUser {
			return false, nil
		}
		entry, err := json.Marshal(piOAuth{
			Type:      "oauth",
			Access:    e.Secret,
			Refresh:   managedRefresh,
			Expires:   e.Expires.UnixMilli(),
			AccountID: e.Account,
		})
		if err != nil {
			return false, err
		}
		entries[key] = entry
		return true, nil
	})
}

// removePiCredential deletes the credential under key from pi's auth.json
// if agents wrote it.
func removePiCredential(path, key string) (removed bool, err error) {
	return changePiAuth(path, func(entries map[string]json.RawMessage) (bool, error) {
		if piOwner(entries[key]) != ownerAgents {
			return false, nil
		}
		delete(entries, key)
		return true, nil
	})
}

// piOAuth is an OAuth login in pi's auth.json.
type piOAuth struct {
	Type      string `json:"type"` // "oauth"
	Access    string `json:"access"`
	Refresh   string `json:"refresh"`
	Expires   int64  `json:"expires"` // milliseconds since 1970
	AccountID string `json:"accountId"`
}

// owner says who put a login in a harness's file.
type owner int

const (
	ownerNone owner = iota // no login under the key
	ownerAgents
	ownerUser
)

// piOwner says who wrote entry, an auth.json value. It's decoded leniently:
// the file is pi's, and pi may add fields.
func piOwner(entry json.RawMessage) owner {
	if entry == nil {
		return ownerNone
	}
	var e struct {
		Refresh string `json:"refresh"`
	}
	if json.Unmarshal(entry, &e) == nil && e.Refresh == managedRefresh {
		return ownerAgents
	}
	return ownerUser
}

// changePiAuth changes pi's auth.json at path under pi's own lock. change
// gets every entry and returns whether to save; changePiAuth reports
// whether it saved. Entries change doesn't touch are written back exactly
// as they were read.
func changePiAuth(path string, change func(map[string]json.RawMessage) (save bool, err error)) (saved bool, err error) {
	unlock, err := lockLikeProperLockfile(path)
	if err != nil {
		return false, err
	}
	defer unlock()
	// A crashed write's temp file holds an access token, so a revoked
	// login must not survive in one. (pi writes in place, without temp files.)
	if err := atomicfile.RemoveLeftovers(path); err != nil {
		return false, err
	}
	entries, err := readPiAuth(path)
	if err != nil {
		return false, err
	}
	save, err := change(entries)
	if err != nil || !save {
		return false, err
	}
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return false, err
	}
	if err := atomicfile.Write(path, append(data, '\n'), 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// maxCredentialFile is the most agents reads of a auth file: far more than any
// real one, so a planted huge file can't exhaust memory.
const maxCredentialFile = 1 << 20

// readPiAuth reads pi's auth.json. A missing file has no entries. The file
// is inside a profile, which a harness (and, later, a sandbox) can write to,
// so it only reads a regular file: never through a symlink (a link to
// another auth.json would copy that file's logins into this profile), and
// never a named pipe or device (which could block agents).
func readPiAuth(path string) (map[string]json.RawMessage, error) {
	// O_NONBLOCK: opening a planted named pipe would otherwise wait forever.
	// The regular-file check below then refuses it.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return map[string]json.RawMessage{}, nil
	case errors.Is(err, syscall.ELOOP):
		return nil, fmt.Errorf("%s is a symlink: %w", path, errNotRegular)
	case err != nil:
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s %w", path, errNotRegular)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxCredentialFile))
	if err != nil {
		return nil, err
	}
	entries := map[string]json.RawMessage{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &entries); err != nil {
			// Not err: it can quote the file, which holds secrets.
			return nil, fmt.Errorf("%s isn't valid JSON; fix it or delete it and log in again", path)
		}
	}
	return entries, nil
}

// breakStaleLock removes a lock left by a crash. It renames the lock to a
// name only this process uses before removing it, which narrows the race
// when two processes find the same stale lock but doesn't close it: one can
// still rename a lock the other has just taken, and both go ahead. That's
// the same as proper-lockfile, and agents' own writers can't race each
// other here (they hold the profile's lock), so only pi could, and only
// after a crash left a stale lock.
func breakStaleLock(lock string) {
	stale := fmt.Sprintf("%s.stale-%d-%d", lock, os.Getpid(), time.Now().UnixNano())
	if os.Rename(lock, stale) == nil {
		_ = os.Remove(stale)
	}
}

// How long lockLikeProperLockfile waits for the lock, and when it treats a
// lock as left behind by a crash. proper-lockfile refreshes a held lock's
// time every few seconds and pi counts a lock as stale after 30 seconds.
const (
	lockWait  = 10 * time.Second
	lockStale = 30 * time.Second
)

// lockLikeProperLockfile takes the lock pi takes on path: the folder
// path+".lock", which only one process can create. It waits while another
// process holds it, and breaks a lock older than lockStale.
func lockLikeProperLockfile(path string) (unlock func(), err error) {
	lock := path + ".lock"
	deadline := time.Now().Add(lockWait)
	for {
		mkdirErr := os.Mkdir(lock, 0o700)
		if mkdirErr == nil {
			return func() { _ = os.Remove(lock) }, nil
		}
		if !errors.Is(mkdirErr, os.ErrExist) {
			return nil, mkdirErr
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is locked by another program (is pi saving a login?); try again, or delete %s if nothing is running", path, lock)
		}
		if info, statErr := os.Lstat(lock); statErr == nil && time.Since(info.ModTime()) > lockStale {
			breakStaleLock(lock)
			continue
		}
		time.Sleep(50 * time.Millisecond)
	}
}
