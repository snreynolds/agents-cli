package harness

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CopySetup copies the harness's setup files from one config folder (src) to
// another (dst), for `agents new --from`. Only SetupFiles are copied, and
// entries src doesn't have are ignored. Login files (SecretFiles) and
// symlinks are skipped wherever they appear, and DropJSONKeys are removed
// from JSON files. It returns a warning for each login file, symlink or
// unparseable JSON file it skipped (devices and sockets are skipped quietly).
func (s Spec) CopySetup(src, dst string) ([]string, error) {
	var warnings []string
	for _, entry := range s.SetupFiles {
		root := filepath.Join(src, entry)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if path == root && errors.Is(err, fs.ErrNotExist) {
				return nil // src doesn't have this entry
			}
			if err != nil {
				return err
			}
			name, _ := filepath.Rel(src, path)
			skipped, err := s.copyEntry(path, filepath.Join(dst, name), name, d)
			if skipped != "" {
				warnings = append(warnings, fmt.Sprintf("skipped %s (%s)", name, skipped))
				if d.IsDir() {
					return filepath.SkipDir
				}
			}
			return err
		})
		if err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// copyEntry copies one file or makes one folder found by CopySetup's walk
// (WalkDir never follows symlinks). It returns why the entry was skipped, or
// "" if it was copied or isn't worth a warning (devices, sockets).
//
// Files are written in place rather than atomically: dst is a brand-new
// profile, so a crash can't leave a half-written copy of anything that matters.
func (s Spec) copyEntry(from, to, name string, d fs.DirEntry) (skipped string, err error) {
	switch {
	case slices.Contains(s.SecretFiles, d.Name()):
		return "login files are never copied", nil
	case d.Type()&fs.ModeSymlink != 0:
		return "symlinks are never copied", nil
	case d.IsDir():
		return "", os.MkdirAll(to, 0o700)
	case !d.Type().IsRegular():
		return "", nil
	}
	data, err := os.ReadFile(from)
	if err != nil {
		return "", err
	}
	if keys := s.DropJSONKeys[name]; len(keys) > 0 {
		if data, err = dropJSONKeys(data, keys); err != nil {
			return fmt.Sprintf("couldn't remove %s from it: %v", strings.Join(keys, ", "), err), nil
		}
	}
	info, err := d.Info()
	if err != nil {
		return "", err
	}
	// Keep the owner's permission bits (so scripts stay executable); nobody
	// else gets access.
	return "", os.WriteFile(to, data, (info.Mode().Perm()&0o700)|0o600)
}

// AddJSONListItems adds the harness's JSONListItems to a new profile's config
// folder dir, after CopySetup, replacing "${HOME}" with home. An item already
// in its list isn't added twice, and a file with nothing to add isn't
// rewritten. A file that isn't a JSON object, or a key that isn't a list, is
// left as it is, with a warning. Like copyEntry, it writes in place: dir
// belongs to a brand-new profile.
func (s Spec) AddJSONListItems(dir, home string) (warnings []string, err error) {
	for _, name := range slices.Sorted(maps.Keys(s.JSONListItems)) {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			data = []byte("{}")
		case err != nil:
			return warnings, err
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(data, &obj) != nil || obj == nil {
			warnings = append(warnings, fmt.Sprintf("didn't change %s (not a JSON object)", name))
			continue
		}
		lists, changed := s.JSONListItems[name], false
		for _, key := range slices.Sorted(maps.Keys(lists)) {
			var list []any
			if raw, ok := obj[key]; ok && json.Unmarshal(raw, &list) != nil {
				warnings = append(warnings, fmt.Sprintf("didn't change %s in %s (not a list)", key, name))
				continue
			}
			for _, item := range lists[key] {
				item = strings.ReplaceAll(item, "${HOME}", home)
				if !slices.Contains(list, any(item)) {
					list, changed = append(list, item), true
				}
			}
			if obj[key], err = json.Marshal(list); err != nil {
				return warnings, err
			}
		}
		if !changed {
			continue
		}
		out, err := json.MarshalIndent(obj, "", "  ")
		if err != nil {
			return warnings, err
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// dropJSONKeys removes top-level keys from a JSON object. The result has its
// keys in sorted order.
func dropJSONKeys(data []byte, keys []string) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, errors.New("not a JSON object")
	}
	for _, k := range keys {
		delete(obj, k)
	}
	out, err := json.MarshalIndent(obj, "", "  ")
	return append(out, '\n'), err
}
