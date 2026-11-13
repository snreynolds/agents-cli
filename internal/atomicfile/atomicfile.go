// Package atomicfile writes files so a crash never leaves a partial file.
package atomicfile

import (
	"os"
	"path/filepath"
)

// Write replaces path with data. It writes a temp file in the same
// folder, syncs it, renames it over path and syncs the folder, so a crash
// leaves either the old file or the new one, never a partial file, and a
// saved file survives a power loss.
func Write(path string, data []byte, perm os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), tempPattern(path))
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// Best-effort cleanup; the original error is the one that matters.
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// RemoveLeftovers deletes temp files that a Write to path left behind when
// it crashed: they can hold copies of secrets. Call it only while no Write
// to path is in progress, e.g. under the lock that guards path.
func RemoveLeftovers(path string) error {
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), tempPattern(path)))
	if err != nil {
		return err
	}
	for _, f := range leftovers {
		if info, err := os.Lstat(f); err != nil || !info.Mode().IsRegular() {
			continue // Write only leaves regular files; anything else isn't its
		}
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	return nil
}

// tempPattern names Write's temp files for path: hidden, next to it.
func tempPattern(path string) string { return "." + filepath.Base(path) + ".tmp-*" }

// syncDir makes a rename in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}
