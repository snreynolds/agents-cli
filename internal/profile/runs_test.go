package profile

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStore_runs(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "profiles"))
	if err := s.Create(Profile{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	first, err := s.AddRun("fast", RunRecord{PID: 1, Start: 10, Dir: "/a"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AddRun("fast", RunRecord{PID: 2, Start: 20, Dir: "/b"})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || first.ID == second.ID {
		t.Errorf("IDs %q and %q, want distinct", first.ID, second.ID)
	}

	runs, err := s.Runs("fast")
	if err != nil || len(runs) != 2 {
		t.Fatalf("Runs = %+v, %v; want both", runs, err)
	}
	for _, r := range runs {
		if r != first && r != second {
			t.Errorf("Runs returned %+v, which wasn't written", r)
		}
	}

	if err := s.RemoveRun("fast", first.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveRun("fast", first.ID); err != nil {
		t.Errorf("removing a removed record: %v", err)
	}
	if runs, _ := s.Runs("fast"); len(runs) != 1 {
		t.Errorf("Runs after RemoveRun = %+v", runs)
	}
}

// AddRun recreates a deleted run/ folder, but never a deleted profile.
func TestStore_addRunMissingFolders(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "profiles"))
	if err := s.Create(Profile{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(filepath.Join(s.root, "fast"), "run")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun("fast", RunRecord{PID: 1}); err != nil {
		t.Errorf("run/ deleted: %v", err)
	}
	if err := s.Remove("fast"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRun("fast", RunRecord{PID: 1}); err == nil || s.Exists("fast") {
		t.Error("AddRun recreated a removed profile")
	}
}

// Lock waits for the holder, and fails if the profile was removed meanwhile.
func TestStore_lock(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "profiles"))
	if err := s.Create(Profile{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	unlock, err := s.Lock("fast")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		unlock2, err := s.Lock("fast")
		if err == nil {
			unlock2()
		}
		got <- err
	}()
	select {
	case err := <-got:
		t.Fatalf("second Lock didn't wait (err %v)", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.Remove("fast"); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-got; err == nil {
		t.Error("Lock succeeded on a profile removed while it waited")
	}
	if _, err := s.Lock("nope"); err == nil {
		t.Error("locking a missing profile should fail")
	}
}

// A record's ID is its file name, whatever the file says, and RemoveRun
// refuses IDs that could reach outside run/.
func TestStore_runIDs(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "profiles"))
	if err := s.Create(Profile{Name: "fast", Harness: "pi"}); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(s.root, "fast", "run")
	writeRun := func(file, content string) {
		if err := os.WriteFile(filepath.Join(runDir, file), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeRun("abc.json", `{"pid": 1}`)
	writeRun("old.json", `{"id": "../../../evil", "pid": 1}`) // unknown field: unreadable, so ignored
	writeRun("..json", `{"pid": 1}`)                          // hidden: never a run ID
	runs, err := s.Runs("fast")
	if err != nil || len(runs) != 1 || runs[0].ID != "abc" {
		t.Fatalf("Runs = %+v, %v; want only abc, ID from the file name", runs, err)
	}
	for _, id := range []string{"", "../x", "a/b", ".hidden"} {
		if err := s.RemoveRun("fast", id); err == nil {
			t.Errorf("RemoveRun(%q) succeeded", id)
		}
	}
}
