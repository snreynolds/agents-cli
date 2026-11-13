package credential

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	s := NewStore(dir)
	if got, err := s.Load(); err != nil || len(got) != 0 {
		t.Fatalf("Load before any save = %v, %v; want empty", got, err)
	}

	added := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	if replaced, err := s.set("openai", Entry{Secret: "sk-1", Added: added}); err != nil || replaced {
		t.Fatalf("first Set = %v, %v", replaced, err)
	}
	if replaced, err := s.set("openai", Entry{Secret: "sk-2", Added: added}); err != nil || !replaced {
		t.Fatalf("second Set = %v, %v; want replaced", replaced, err)
	}
	got, err := s.Load()
	if err != nil || got["openai"].Secret != "sk-2" || !got["openai"].Added.Equal(added) {
		t.Fatalf("Load = %+v, %v", got, err)
	}

	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "credentials.json"): 0o600} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %o", path, info.Mode().Perm(), err, want)
		}
	}

	if removed, err := s.Remove("openai"); err != nil || !removed {
		t.Fatalf("Remove = %v, %v", removed, err)
	}
	if removed, err := s.Remove("openai"); err != nil || removed {
		t.Errorf("second Remove = %v, %v; want not removed, no error", removed, err)
	}
}

// A field this version doesn't know makes the file unreadable rather than
// silently dropped on the next save.
func TestStore_Load_unknownField(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"openai": {"secret": "x", "nickname": "y"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Load(); err == nil {
		t.Error("Load accepted an unknown field")
	}
}

// Saves from many goroutines at once all land: the lock serializes them.
func TestStore_set_concurrent(t *testing.T) {
	s := NewStore(t.TempDir())
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Go(func() {
			if _, err := s.set(id, Entry{Secret: id}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got, _ := s.Load(); len(got) != len(ids) {
		t.Errorf("saved %d of %d", len(got), len(ids))
	}
}

// A save tightens an existing folder's permissions and removes temp files a
// crashed save left behind, since they hold secrets.
func TestStore_set_cleansUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "credentials")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	leftover := filepath.Join(dir, ".credentials.json.tmp-123")
	if err := os.WriteFile(leftover, []byte(`{"openai": {"secret": "old"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).set("openai", Entry{Secret: "new"}); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %o, want 700", info.Mode().Perm())
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Error("leftover temp file wasn't removed")
	}
}

// A damaged file's error never quotes its contents.
func TestStore_Load_damagedDoesntEcho(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte(`{"openai": {"secret": "x", "added": "sk-LEAK"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(dir).Load()
	if err == nil || strings.Contains(err.Error(), "sk-LEAK") {
		t.Errorf("got %v, want an error without the file's contents", err)
	}
}

// A file holding null is damaged, not a crash on the next save (QA Q16).
func TestStore_Load_null(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), []byte("null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir).Load(); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("Load = %v, want a damaged-file error", err)
	}
}

// Update changes one login under the lock: nothing is saved if the change
// fails, a missing login is ErrNotFound, and concurrent updates take turns
// (so two renewals can't both spend a single-use refresh token).
func TestStore_updateEntry(t *testing.T) {
	s := NewStore(filepath.Join(t.TempDir(), "credentials"))
	if _, err := s.updateEntry("chatgpt", func(e Entry) (Entry, error) { return e, nil }); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update of a missing login = %v, want ErrNotFound", err)
	}
	if _, err := s.set("chatgpt", Entry{Secret: "a0", Refresh: "r0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.updateEntry("chatgpt", func(e Entry) (Entry, error) {
		e.Secret = "changed"
		return e, errors.New("refresh failed")
	}); err == nil {
		t.Error("want the change's error")
	}
	if got, _ := s.Load(); got["chatgpt"].Secret != "a0" {
		t.Errorf("a failed change was saved: %+v", got["chatgpt"])
	}

	// Each update reads the previous one's result: spent refresh tokens are
	// never reused.
	var wg sync.WaitGroup
	var reused atomic.Bool
	var spent sync.Map
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.updateEntry("chatgpt", func(e Entry) (Entry, error) {
				if _, loaded := spent.LoadOrStore(e.Refresh, true); loaded {
					reused.Store(true)
				}
				e.Refresh += "+"
				return e, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if reused.Load() {
		t.Error("two updates spent the same refresh token")
	}
	if got, _ := s.Load(); got["chatgpt"].Refresh != "r0++++++++++" {
		t.Errorf("refresh = %q, want ten updates applied in turn", got["chatgpt"].Refresh)
	}
}

func TestStore_Save(t *testing.T) {
	s := NewStore(t.TempDir())
	openai, _ := Supported().Find("openai")
	if replaced, err := s.Save(openai, Entry{Secret: "sk-1"}); err != nil || replaced {
		t.Fatalf("Save = %v, %v", replaced, err)
	}
	if replaced, err := s.Save(openai, Entry{Secret: "sk-2"}); err != nil || !replaced {
		t.Fatalf("second Save = %v, %v; want replaced", replaced, err)
	}
	if saved, _ := s.Load(); saved["openai"].Added.IsZero() {
		t.Error("Save didn't set Added")
	}
	chatgpt, _ := Supported().Find("chatgpt")
	claude, _ := Supported().Find("claude")
	for name, c := range map[string]struct {
		spec Spec
		e    Entry
		want string
	}{
		"empty":                {openai, Entry{}, "nothing was entered"},
		"two lines":            {openai, Entry{Secret: "sk-1\nsk-2"}, "no spaces"},
		"space inside":         {openai, Entry{Secret: "sk-1 extra"}, "no spaces"},
		"NUL":                  {openai, Entry{Secret: "sk-1\x00"}, "no spaces"},
		"key with a refresh":   {openai, Entry{Secret: "sk-1", Refresh: "rt"}, "no refresh token"},
		"token with expiry":    {claude, Entry{Secret: "t", Expires: time.Now()}, "no refresh token"},
		"pasted OAuth":         {chatgpt, Entry{Secret: "pasted"}, "signs in with your browser"},
		"OAuth without expiry": {chatgpt, Entry{Secret: "a", Refresh: "rt"}, "signs in with your browser"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Save(c.spec, c.e); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Save = %v, want %q", err, c.want)
			}
		})
	}
}

// fakeRenewer renews by appending "+" to the refresh token, or fails.
type fakeRenewer struct {
	APIKey
	fail  error
	calls *int
}

func (r fakeRenewer) Renew(_ context.Context, e Entry) (Entry, error) {
	*r.calls++
	if r.fail != nil {
		return Entry{}, r.fail
	}
	return Entry{Secret: "new", Refresh: e.Refresh + "+", Expires: time.Now().Add(10 * 24 * time.Hour), Account: e.Account}, nil
}

func TestStore_Renew(t *testing.T) {
	s := NewStore(t.TempDir())
	calls := 0
	c := Spec{ID: "sub", Source: fakeRenewer{calls: &calls}}
	if _, err := s.Renew(t.Context(), c); !errors.Is(err, ErrNotFound) {
		t.Errorf("renewing an unsaved credential: %v, want ErrNotFound", err)
	}
	added := time.Now().Add(-time.Hour).UTC()
	if _, err := s.set("sub", Entry{Secret: "old", Refresh: "rt", Expires: time.Now().Add(time.Minute), Added: added}); err != nil {
		t.Fatal(err)
	}
	e, err := s.Renew(t.Context(), c)
	if err != nil || e.Secret != "new" || e.Refresh != "rt+" || !e.Added.Equal(added) {
		t.Errorf("Renew: secret %q, refresh %q, added %v, err %v", e.Secret, e.Refresh, e.Added, err)
	}
	if _, err := s.Renew(t.Context(), c); err != nil || calls != 1 {
		t.Errorf("a fresh credential was renewed again (%d calls, %v)", calls, err)
	}
	if _, err := s.set("sub", Entry{Secret: "old", Expires: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Renew(t.Context(), c); !errors.Is(err, errNoRefresh) || calls != 1 {
		t.Errorf("no refresh token: %v (%d calls), want errNoRefresh and no request", err, calls)
	}
	if _, err := s.Renew(t.Context(), Spec{ID: "openai", Source: APIKey{}}); err == nil {
		t.Error("an API key isn't renewed")
	}
}
