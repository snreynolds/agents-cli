package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/snreynolds/agents-cli/internal/credential"
	"github.com/snreynolds/agents-cli/internal/harness"
	"github.com/snreynolds/agents-cli/internal/profile"
)

// This file is the `agents auth` actions. Everything about a credential
// itself (signing in, storing, renewing, writing auth files) is in the
// credential package; app connects it to harnesses, profiles and the user.

// SupportedCredentials returns every credential agents supports, in picker
// order.
func (a *App) SupportedCredentials() []credential.Spec {
	return slices.Clone(a.catalog)
}

// CredentialHarnesses returns, for each credential ID, the installed
// harnesses that support it, sorted. Like harness.Spec.Credentials, it
// doesn't consider whether the credential is saved or allowed by a profile.
func (a *App) CredentialHarnesses() (map[string][]string, error) {
	reg, err := a.harnesses()
	if err != nil {
		return nil, err
	}
	supportedBy := map[string][]string{}
	for _, spec := range reg.Specs() {
		for id := range spec.Credentials {
			supportedBy[id] = append(supportedBy[id], spec.Name)
		}
	}
	return supportedBy, nil
}

// SignIn gets the credential with this ID by its own way of signing in
// (pasting a key, running `claude setup-token`, or the ChatGPT sign-in),
// talking to the user through ui, and saves it, replacing any saved under
// the same ID. Runs started from then on get it.
func (a *App) SignIn(ctx context.Context, id string, ui credential.UI) (replaced bool, err error) {
	c, err := a.catalog.Lookup(id)
	if err != nil {
		return false, err
	}
	e, err := c.Source.SignIn(ctx, c, ui)
	if err != nil {
		return false, err
	}
	return a.credentials.Save(c, e)
}

// SavedCredential is one saved credential as `agents auth list` shows it:
// never the secret.
type SavedCredential struct {
	ID    string    `json:"id"`
	Added time.Time `json:"added"`
}

// ListCredentials returns the saved credentials, sorted by ID.
func (a *App) ListCredentials() ([]SavedCredential, error) {
	saved, err := a.credentials.Load()
	if err != nil {
		return nil, err
	}
	list := []SavedCredential{} // empty, not nil, so JSON output is [] rather than null
	for id, e := range saved {
		list = append(list, SavedCredential{ID: id, Added: e.Added})
	}
	slices.SortFunc(list, func(x, y SavedCredential) int { return strings.Compare(x.ID, y.ID) })
	return list, nil
}

// Revocation is what RevokeCredential did.
type Revocation struct {
	Removed     bool      // it was saved, and now isn't (false if it already wasn't)
	ClearedFrom []string  // profiles whose auth file it was removed from
	Unchecked   []string  // profiles agents couldn't read, so may still hold it
	Running     []LiveRun // live runs that may still have it in their env vars
}

// LiveRun is a live run that may still have a revoked credential.
type LiveRun struct {
	Profile string
	PID     int
	Dir     string
}

// RevokeCredential deletes a saved credential, so runs started from then on
// don't get it. A credential given through a harness's auth file is also
// removed from every profile's file it was written into (not the user's own
// login there), so running sessions lose it when they next read the file;
// this is done even if the credential is no longer saved, so running revoke
// again finishes a revoke that failed partway. A credential given in an env
// var can't be taken from a running harness, so those runs are listed. Any
// saved ID can be revoked, even one this version no longer supports.
func (a *App) RevokeCredential(id string) (Revocation, error) {
	saved, err := a.credentials.Load()
	if err != nil {
		return Revocation{}, err
	}
	var r Revocation
	if _, ok := saved[id]; ok {
		if r.Removed, err = a.credentials.Remove(id); err != nil {
			return r, err
		}
	}
	// Only renewable credentials can be in a auth file (harness validation
	// makes sure), so no other kind needs the profiles' files checked.
	c, supported := a.catalog.Find(id)
	inFiles := supported && c.Renews()
	if !r.Removed && !inFiles {
		_, idErr := a.catalog.Lookup(id) // nothing to do; a typo is worth a suggestion
		return r, idErr
	}
	reg, err := a.harnesses()
	var profiles []profile.Profile
	var unreadable []string
	if err == nil {
		profiles, unreadable, err = a.profiles.List()
	}
	if err != nil {
		if r.Removed {
			return r, fmt.Errorf("removed %s, but couldn't check the profiles: %w", id, err)
		}
		return r, fmt.Errorf("couldn't check the profiles for %s: %w", id, err)
	}
	if inFiles {
		if r.ClearedFrom, r.Unchecked, err = a.unprovisionFromProfiles(id, reg, profiles, unreadable); err != nil {
			return r, fmt.Errorf("couldn't remove %s from every profile; fix these, then run agents auth revoke %s again:\n%w", id, id, err)
		}
	}
	if r.Removed {
		if r.Running, err = a.runningWith(id, saved, reg, profiles); err != nil {
			return r, fmt.Errorf("removed %s, but couldn't check for running profiles: %w", id, err)
		}
	}
	return r, nil
}

// unprovisionFromProfiles removes the credential with this ID from every
// profile's auth file that agents wrote it into, and returns those
// profiles, plus the profiles it couldn't look at: the unreadable ones, and
// ones whose harness is gone (a deleted harness file may have kept the
// credential in a file). It carries on past a profile it can't clear and
// reports every failure. Each profile is locked meanwhile, so a run starting
// at the same moment can't write the credential back (see
// provisionAuthFiles).
func (a *App) unprovisionFromProfiles(id string, reg *harness.Registry, profiles []profile.Profile, unreadable []string) (cleared, unchecked []string, err error) {
	unchecked = slices.Clone(unreadable)
	var problems []error
	for _, p := range profiles {
		spec, specErr := reg.Get(p.Harness)
		if specErr != nil {
			unchecked = append(unchecked, p.Name)
			continue
		}
		if spec.Credentials[id].AuthFile == "" {
			continue
		}
		removed, clearErr := a.unprovisionFromProfile(p.Name, spec, id)
		if clearErr != nil {
			problems = append(problems, fmt.Errorf("%s: %w", p.Name, clearErr))
			continue
		}
		if removed {
			cleared = append(cleared, p.Name)
		}
	}
	slices.Sort(unchecked)
	return cleared, unchecked, errors.Join(problems...)
}

// unprovisionFromProfile removes one credential from one profile's login
// file, with the profile locked.
func (a *App) unprovisionFromProfile(name string, spec harness.Spec, id string) (bool, error) {
	unlock, err := a.profiles.Lock(name)
	if err != nil {
		return false, err
	}
	defer unlock()
	return credential.Unprovision(harnessAuthFile(a.profiles.ConfigDir(name), spec.Credentials[id]))
}

// runningWith returns the live runs of profiles that would have been given
// the credential in an env var, judged by the saved credentials before it
// was removed. A run that started under different settings may differ:
// agents doesn't record which credentials each run actually got.
func (a *App) runningWith(id string, saved map[string]credential.Entry, reg *harness.Registry, profiles []profile.Profile) ([]LiveRun, error) {
	var running []LiveRun
	for _, p := range profiles {
		spec, specErr := reg.Get(p.Harness)
		if specErr != nil {
			continue // its runs can't have this credential
		}
		if spec.Credentials[id].Env == "" {
			continue // not given in an env var by this harness
		}
		if granted, _ := grantCredentials(p, spec, saved); !slices.Contains(granted, id) {
			continue
		}
		runs, runsErr := a.liveRuns(p.Name)
		if runsErr != nil {
			return nil, runsErr
		}
		for _, r := range runs {
			running = append(running, LiveRun{Profile: p.Name, PID: r.PID, Dir: r.Dir})
		}
	}
	return running, nil
}
