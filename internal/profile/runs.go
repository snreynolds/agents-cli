package profile

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/snreynolds/agents-cli/internal/atomicfile"
)

// RunRecord is run/<id>.json in the profile's folder, written while a run
// lasts. A record can outlive its run (e.g. after a crash); deciding whether
// a run is still going is up to the caller.
type RunRecord struct {
	ID    string `json:"-"` // the file name, never read from the file's contents
	PID   int    `json:"pid"`
	Start int64  `json:"start"` // from process.StartTime, to tell a reused PID apart
	Dir   string `json:"dir"`   // the folder the harness runs in
}

// runDir returns the profile's run/ folder (see checkedDir).
func (s *Store) runDir(name string) (string, error) {
	dir, err := s.checkedDir(name)
	return filepath.Join(dir, "run"), err
}

// AddRun writes a record for a run that has just started and returns it with
// its new ID.
func (s *Store) AddRun(name string, r RunRecord) (RunRecord, error) {
	runDir, err := s.runDir(name)
	if err != nil {
		return RunRecord{}, err
	}
	r.ID = newRunID()
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return RunRecord{}, err
	}
	// Recreate run/ if it was deleted by hand, but never the profile itself.
	if err := os.Mkdir(runDir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return RunRecord{}, err
	}
	return r, atomicfile.Write(filepath.Join(runDir, r.ID+".json"), data, 0o600)
}

// RemoveRun deletes a run record. A record that is already gone is not an
// error.
func (s *Store) RemoveRun(name, id string) error {
	runDir, err := s.runDir(name)
	if err != nil {
		return err
	}
	if !validRunID(id) {
		return fmt.Errorf("invalid run ID %q", id)
	}
	err = os.Remove(filepath.Join(runDir, id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Runs returns the profile's run records. Unreadable files are
// skipped: records are written atomically, so one that can't be read wasn't
// written by agents.
func (s *Store) Runs(name string) ([]RunRecord, error) {
	runDir, err := s.runDir(name)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(runDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var records []RunRecord
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !validRunID(id) { // also skips atomicfile's hidden temp files
			continue
		}
		if r, err := loadRun(filepath.Join(runDir, e.Name())); err == nil {
			r.ID = id
			records = append(records, r)
		}
	}
	return records, nil
}

func loadRun(path string) (RunRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RunRecord{}, err
	}
	var r RunRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	err = dec.Decode(&r)
	return r, err
}

// validRunID reports whether id could name a run record: a plain file name,
// not hidden, so it can never reach outside run/.
func validRunID(id string) bool {
	return id != "" && id == filepath.Base(id) && !strings.HasPrefix(id, ".")
}

// newRunID returns a random ID for a run record.
func newRunID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b) // never fails (see crypto/rand.Read)
	return hex.EncodeToString(b)
}
