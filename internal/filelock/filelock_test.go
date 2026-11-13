package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A second Lock waits until the first is released.
func TestLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock")
	unlock, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		unlock2, err := Lock(path)
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
	unlock()
	if err := <-got; err != nil {
		t.Errorf("second Lock after release: %v", err)
	}
}

func TestLock_missingFolder(t *testing.T) {
	if _, err := Lock(filepath.Join(t.TempDir(), "gone", "lock")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("got %v, want an os.ErrNotExist error", err)
	}
}
