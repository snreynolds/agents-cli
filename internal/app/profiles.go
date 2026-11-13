package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"github.com/snreynolds/agents-cli/internal/harness"
	"github.com/snreynolds/agents-cli/internal/profile"
)

// CreateOptions are the inputs to CreateProfile (`agents new`).
type CreateOptions struct {
	Name, Harness      string
	From               string   // optional config folder to copy setup from
	AllowedCredentials []string // nil = all, empty = none
}

// Created describes a newly created profile.
type Created struct {
	ConfigDir string   // the harness's config folder for this profile
	Warnings  []string // from --from
}

// CreateProfile creates a profile for `agents new`.
func (a *App) CreateProfile(o CreateOptions) (Created, error) {
	spec, allow, err := a.checkCreateOptions(o)
	if err != nil {
		return Created{}, err
	}
	if err := a.profiles.Create(profile.Profile{Name: o.Name, Harness: o.Harness, AllowedCredentials: allow}); err != nil {
		return Created{}, err
	}
	created := Created{ConfigDir: a.profiles.ConfigDir(o.Name)}
	if created.Warnings, err = setUpConfig(spec, o.From, created.ConfigDir); err != nil {
		// Don't leave a half-made profile behind.
		return Created{}, errors.Join(err, a.profiles.Remove(o.Name))
	}
	return created, nil
}

// setUpConfig fills a new profile's config folder: the setup files copied
// from the from folder (if any), then the harness's JSONListItems, which
// keep out folders the harness would otherwise share between profiles.
func setUpConfig(spec harness.Spec, from, configDir string) (warnings []string, err error) {
	if from != "" {
		if warnings, err = spec.CopySetup(from, configDir); err != nil {
			return warnings, err
		}
	}
	if len(spec.JSONListItems) == 0 {
		return warnings, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return warnings, err
	}
	added, err := spec.AddJSONListItems(configDir, home)
	return append(warnings, added...), err
}

// checkCreateOptions validates `agents new` input and resolves the harness and
// the credential allow list. It reports every problem at once, not just the
// first.
func (a *App) checkCreateOptions(o CreateOptions) (harness.Spec, []string, error) {
	problems := []error{
		// Store.Create checks the name too, for safety; this check is so the
		// user sees every problem at once.
		profile.ValidateName(o.Name),
		checkFromFolder(o.From),
	}
	reg, err := a.harnesses()
	if err != nil {
		return harness.Spec{}, nil, errors.Join(append(problems, err)...)
	}
	spec, err := reg.Get(o.Harness)
	if err != nil {
		return harness.Spec{}, nil, errors.Join(append(problems, err)...)
	}
	allow, err := allowedCredentials(spec, o.AllowedCredentials)
	return spec, allow, errors.Join(append(problems, err)...) // nil if there were no problems
}

// ProfileStatus is one profile as `agents ls` shows it.
type ProfileStatus struct {
	Name               string   `json:"name"`
	Harness            string   `json:"harness"`
	AllowedCredentials []string `json:"allowed_credentials"` // ["*"] = all
	Running            int      `json:"running"`             // live runs
}

// ListProfiles returns every profile with its live run count, sorted by name, for
// `agents ls`, plus the names of any profiles whose profile.toml can't be read.
func (a *App) ListProfiles() (statuses []ProfileStatus, unreadable []string, err error) {
	profiles, unreadable, err := a.profiles.List()
	if err != nil {
		return nil, nil, err
	}
	statuses = make([]ProfileStatus, len(profiles))
	for i, p := range profiles {
		runs, err := a.liveRuns(p.Name)
		if err != nil {
			return nil, nil, err
		}
		allow := p.AllowedCredentials
		if allow == nil {
			allow = []string{} // JSON output is [], not null
		}
		statuses[i] = ProfileStatus{Name: p.Name, Harness: p.Harness, AllowedCredentials: allow, Running: len(runs)}
	}
	return statuses, unreadable, nil
}

// CheckRemoveProfile reports why a profile can't be removed: it doesn't exist,
// or it's running. Commands call it before asking for confirmation, so the
// user isn't asked about something that will be refused. The answer can go
// stale while they reply; RemoveProfile checks again under the lock.
func (a *App) CheckRemoveProfile(name string) error {
	if !a.profiles.Exists(name) {
		return fmt.Errorf("%w: %s", profile.ErrNotFound, name)
	}
	return a.checkNotRunning(name)
}

// RemoveProfile deletes a profile and everything in it: settings, sessions
// and logins written to it. It fails with profile.ErrNotFound if there's no
// such profile, and refuses while the profile is running.
func (a *App) RemoveProfile(name string) error {
	unlock, err := a.profiles.Lock(name) // also checks the profile exists
	if err != nil {
		return err
	}
	defer unlock()
	// Holding the lock, no run can start until the profile is gone.
	if err := a.checkNotRunning(name); err != nil {
		return err
	}
	return a.profiles.Remove(name)
}

// checkNotRunning fails if any run of the profile is live.
func (a *App) checkNotRunning(name string) error {
	runs, err := a.liveRuns(name)
	if err != nil {
		return err
	}
	if len(runs) > 0 {
		return fmt.Errorf("profile %s is running (process %d in %s); stop it first", name, runs[0].PID, runs[0].Dir)
	}
	return nil
}

// allowedCredentials returns the profile's allow list: "*" (every credential
// the harness supports) if ids is nil or just "*", none if it's empty but not
// nil (--credentials ""), otherwise ids sorted and without duplicates. It fails on an ID the harness can't use, or on "*" mixed
// with specific IDs.
func allowedCredentials(spec harness.Spec, ids []string) ([]string, error) {
	switch {
	case ids == nil:
		return []string{"*"}, nil // not given = all
	case len(ids) == 0:
		return []string{}, nil // given empty = none
	}
	if slices.Contains(ids, "*") {
		if len(ids) > 1 {
			return nil, errors.New(`--credentials: use "*" alone for all credentials, or name specific ones (e.g. openai,openrouter), not both`)
		}
		return []string{"*"}, nil
	}
	for _, id := range ids {
		if _, ok := spec.Credentials[id]; !ok {
			return nil, fmt.Errorf("%s doesn't support credential %q", spec.Name, id)
		}
	}
	allow := slices.Clone(ids) // sorting in place would change the caller's slice
	slices.Sort(allow)
	return slices.Compact(allow), nil // drop duplicates
}

// checkFromFolder checks that --from, if given, is an existing folder.
func checkFromFolder(path string) error {
	if path == "" {
		return nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("--from %s: no such folder", path)
	}
	if err != nil {
		return fmt.Errorf("--from: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("--from %s: not a folder", path)
	}
	return nil
}
