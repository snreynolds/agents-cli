// Package filelock lets separate agents processes take turns: one holds a
// lock file at a time, and the others wait. The operating system releases
// the lock when its holder closes the file or exits, even by crashing, so a
// lock can never be left held.
package filelock

import (
	"os"
	"syscall"
)

// Lock creates path if needed (mode 0600) and takes the lock on it, waiting
// while another process holds it. It returns a function that releases it.
// It never creates folders: if path's folder doesn't exist, it fails with an
// error matching os.ErrNotExist.
func Lock(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil // closing releases the lock
}
