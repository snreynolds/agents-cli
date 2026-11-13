package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"slices"
	"time"

	"github.com/snreynolds/agents-cli/internal/credential"
	"github.com/snreynolds/agents-cli/internal/harness"
	"github.com/snreynolds/agents-cli/internal/process"
	"github.com/snreynolds/agents-cli/internal/profile"
)

// RunOptions are the inputs to Run (`agents run`).
type RunOptions struct {
	Name   string
	Args   []string // passed to the harness unchanged
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	Warn   func(string) // called before the harness starts, e.g. "careful gets claude, not anthropic"
}

// Run runs a profile's harness in the current folder, records the run while
// it lasts, and returns the harness's exit code.
func (a *App) Run(o RunOptions) (exitCode int, err error) {
	run, err := a.planHostRun(o.Name, o.Args)
	if err != nil {
		return 0, err
	}
	child, record, err := a.startRecorded(o, run)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = a.profiles.RemoveRun(o.Name, record.ID) // a leftover record is detected as ended
	}()
	return child.Wait()
}

// hostRun is everything needed to start a profile's harness on this machine.
type hostRun struct {
	plan     process.Plan
	spec     harness.Spec
	granted  []string // IDs of the credentials the profile gets
	warnings []string // for the user, before the harness starts
}

// planHostRun checks that the profile can run on this machine and works out
// how to start its harness. On the way it renews, over the network, OAuth
// credentials the profile gets that are close to expiring (see
// renewGranted).
func (a *App) planHostRun(name string, args []string) (hostRun, error) {
	p, err := a.profiles.Load(name)
	if err != nil {
		return hostRun{}, err
	}
	reg, err := a.harnesses()
	if err != nil {
		return hostRun{}, err
	}
	spec, err := reg.Get(p.Harness)
	if err != nil {
		return hostRun{}, fmt.Errorf("profile %s: %w", name, err)
	}
	binary, err := exec.LookPath(spec.Binary)
	if err != nil {
		return hostRun{}, fmt.Errorf("can't start %s (is it installed?): %w", spec.Name, err)
	}
	dir, err := os.Getwd()
	if err != nil {
		return hostRun{}, err
	}
	saved, err := a.credentials.Load()
	if err != nil {
		return hostRun{}, err
	}
	granted, warnings := grantCredentials(p, spec, saved)
	granted, renewWarnings := a.renewGranted(p.Name, granted, saved)

	// The user's environment minus the harness's secret env vars, plus the
	// profile's config folder and the credentials it gets in env vars. Ones
	// it gets in an auth file are written when the run starts.
	set := map[string]string{spec.ConfigEnv: a.profiles.ConfigDir(name)}
	for _, id := range granted {
		if env := spec.Credentials[id].Env; env != "" {
			set[env] = saved[id].Secret
		}
	}
	env := process.Environ(os.Environ(), spec.SecretEnvs, set)
	return hostRun{
		plan:     process.Plan{Binary: binary, Args: args, Dir: dir, Env: env},
		spec:     spec,
		granted:  granted,
		warnings: append(warnings, renewWarnings...),
	}, nil
}

// renewTimeout bounds renewing a credential at the start of a run. It's
// generous: if it ran out after the provider had renewed the credential but
// before agents saved the reply, the saved refresh token would no longer
// work.
const renewTimeout = 30 * time.Second

// renewGranted renews each granted credential that is due
// (see credential.Entry.RenewDue), updating saved, and returns the
// IDs that are still granted. One that can't be renewed is still granted
// while it works, with a warning; once expired, or if it was revoked
// meanwhile, it's dropped. Only granted credentials are renewed, so a
// refresh token is never spent on one the run won't use.
func (a *App) renewGranted(profileName string, granted []string, saved map[string]credential.Entry) (kept []string, warnings []string) {
	for _, id := range granted {
		c, known := a.catalog.Find(id)
		e := saved[id]
		if !known || !c.Renews() || !e.RenewDue() {
			kept = append(kept, id)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), renewTimeout)
		renewed, renewErr := a.credentials.Renew(ctx, c)
		cancel()
		switch {
		case renewErr == nil:
			saved[id] = renewed
			kept = append(kept, id)
		case errors.Is(renewErr, credential.ErrNotFound):
			delete(saved, id) // revoked while this run started
		case time.Now().Before(e.Expires):
			kept = append(kept, id)
			warnings = append(warnings, fmt.Sprintf("couldn't renew the %s login (%v); it works until %s, then run agents auth login %s",
				id, renewErr, e.Expires.Local().Format("Jan 2 15:04"), id))
		default:
			delete(saved, id)
			warnings = append(warnings, fmt.Sprintf("the %s login has expired and couldn't be renewed (%v), so %s doesn't get it; run agents auth login %s",
				id, renewErr, profileName, id))
		}
	}
	return kept, warnings
}

// grantCredentials works out which saved credentials the profile gets:
// those its harness supports, the profile allows and agents has saved, but
// at most one per exclusive group (the first in the group). It returns their
// IDs, sorted, plus a warning for each one held back by a group.
func grantCredentials(p profile.Profile, spec harness.Spec, saved map[string]credential.Entry) (granted []string, warnings []string) {
	supported := slices.Sorted(maps.Keys(spec.Credentials))
	gets := map[string]bool{}
	for _, id := range supported {
		_, ok := saved[id]
		gets[id] = ok && p.AllowsCredential(id)
	}
	for _, group := range spec.ExclusiveCredentials {
		first := ""
		for _, id := range group {
			switch {
			case !gets[id]:
			case first == "":
				first = id
			default:
				gets[id] = false
				warnings = append(warnings, fmt.Sprintf(
					"%s gets %s, not %s (%s uses only one); to use %s instead, create the profile with --credentials %s",
					p.Name, first, id, spec.Name, id, id))
			}
		}
	}
	for _, id := range supported {
		if gets[id] {
			granted = append(granted, id)
		}
	}
	return granted, warnings
}

// startRecorded makes the profile's auth files match its credentials,
// starts the harness and writes its run record, all under the profile's
// lock, so `agents rm` can't delete the profile in between and a revoke
// can't be undone by a run that read the credentials just before it.
func (a *App) startRecorded(o RunOptions, run hostRun) (*process.Child, profile.RunRecord, error) {
	unlock, err := a.profiles.Lock(o.Name)
	if err != nil {
		return nil, profile.RunRecord{}, err
	}
	defer unlock()

	fileWarnings, err := a.provisionAuthFiles(o.Name, run.spec, run.granted)
	if err != nil {
		return nil, profile.RunRecord{}, err
	}
	if o.Warn != nil {
		for _, w := range slices.Concat(run.warnings, fileWarnings) {
			o.Warn(w)
		}
	}
	child, err := process.Start(run.plan, o.Stdin, o.Stdout, o.Stderr)
	if err != nil {
		return nil, profile.RunRecord{}, err
	}
	// This works even if the harness has already exited: until Wait reaps
	// it, its process ID stays taken.
	start, err := process.StartTime(child.PID())
	if err != nil {
		return nil, profile.RunRecord{}, killUnrecorded(child, err)
	}
	record, err := a.profiles.AddRun(o.Name, profile.RunRecord{PID: child.PID(), Start: start, Dir: run.plan.Dir})
	if err != nil {
		return nil, profile.RunRecord{}, killUnrecorded(child, err)
	}
	return child, record, nil
}

// provisionAuthFiles makes the profile's auth files hold exactly the
// credentials it gets through a file: each granted one is provisioned
// (never over the user's own login), and one agents provisioned earlier but
// the profile no longer gets (revoked, or not allowed) is unprovisioned. It
// re-reads the saved credentials, so one revoked since the run was planned
// isn't written back. Failing to provision stops the run; failing to
// unprovision only warns, so a broken auth file doesn't stop profiles
// that don't use it. Call it with the profile locked.
func (a *App) provisionAuthFiles(name string, spec harness.Spec, granted []string) (warnings []string, err error) {
	var inFile []string
	for _, id := range slices.Sorted(maps.Keys(spec.Credentials)) {
		if spec.Credentials[id].AuthFile != "" {
			inFile = append(inFile, id)
		}
	}
	if len(inFile) == 0 {
		return nil, nil
	}
	saved, err := a.credentials.Load()
	if err != nil {
		return nil, err
	}
	configDir := a.profiles.ConfigDir(name)
	for _, id := range inFile {
		f := harnessAuthFile(configDir, spec.Credentials[id])
		e, stillSaved := saved[id]
		if !slices.Contains(granted, id) || !stillSaved {
			if _, err := credential.Unprovision(f); err != nil {
				warnings = append(warnings, fmt.Sprintf("couldn't check %s for an old %s login: %v", name, id, err))
			}
			continue
		}
		written, err := credential.Provision(f, e)
		if err != nil {
			return nil, fmt.Errorf("giving %s the %s login: %w", name, id, err)
		}
		if !written {
			warnings = append(warnings, fmt.Sprintf("%s uses its own %s login (in its %s), not the saved one", name, id, f.File))
		}
	}
	return warnings, nil
}

// harnessAuthFile is where a harness binding says a credential goes, in a
// profile's config folder.
func harnessAuthFile(configDir string, b harness.Binding) credential.HarnessAuthFile {
	return credential.HarnessAuthFile{Dir: configDir, File: b.AuthFile, Format: b.Format, Key: b.Key}
}

// killUnrecorded stops a harness whose run couldn't be recorded: without a
// record, `agents rm` could delete the profile mid-run.
func killUnrecorded(child *process.Child, err error) error {
	child.Kill()
	return fmt.Errorf("recording the run: %w", err)
}

// liveRuns returns the records of the profile's runs that are still going.
// Records of runs that ended without cleaning up (e.g. after a crash) are
// ignored, not deleted: reading never changes anything, and `agents rm`
// removes them with the profile.
func (a *App) liveRuns(name string) ([]profile.RunRecord, error) {
	records, err := a.profiles.Runs(name)
	if err != nil {
		return nil, err
	}
	var live []profile.RunRecord
	for _, r := range records {
		if process.Running(r.PID, r.Start) {
			live = append(live, r)
		}
	}
	return live, nil
}
