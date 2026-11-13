package atomicfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.toml")

	for _, content := range []string{"first", "second"} {
		if err := Write(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("read %q, %v; want %q", got, err, content)
		}
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

// RemoveLeftovers deletes only Write's temp files for that path.
func TestRemoveLeftovers(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".a.json.tmp-1", ".a.json.tmp-2", "a.json", ".b.json.tmp-1"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveLeftovers(filepath.Join(dir, "a.json")); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	var left []string
	for _, e := range entries {
		left = append(left, e.Name())
	}
	if strings.Join(left, ",") != ".b.json.tmp-1,a.json" {
		t.Errorf("left %v, want a.json and the other file's temp file", left)
	}
}
