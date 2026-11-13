// Package harness describes the agent programs agents can run: what to
// start, which of its files are setup (copied by `agents new --from`) and
// which hold secrets, and where it looks for each credential it can use. It
// is description only: it never reads or writes a credential (it checks
// bindings against the credential package's catalog). A harness is data (a
// Spec), so built-ins and user-defined TOML files share one code path.
package harness

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/snreynolds/agents-cli/internal/credential"
)

// Spec describes one harness.
type Spec struct {
	Name      string `toml:"name"`
	Binary    string `toml:"binary"`
	ConfigEnv string `toml:"config_env"` // env var pointing the harness at the profile's config/

	// SetupFiles are what `agents new --from` copies: settings and other setup,
	// never credentials or state. Each is a single file or folder name in the
	// config folder.
	SetupFiles []string `toml:"setup_files"`
	// DropJSONKeys removes top-level keys from JSON setup files while copying
	// them, e.g. pi's sessionDir, which would move sessions out of the profile.
	DropJSONKeys map[string][]string `toml:"drop_json_keys"`
	// JSONListItems are entries `agents new` adds to lists in a new profile's
	// JSON setup files (file → top-level key → items), creating the file or
	// list if needed, so every profile starts fresh. "${HOME}" in an item is
	// the user's home folder. pi uses it to keep out ~/.agents/skills, which
	// it loads in every profile whatever its config folder.
	JSONListItems map[string]map[string][]string `toml:"json_list_items"`
	// SecretFiles are files where the harness keeps logins (e.g. pi's
	// auth.json). `agents new --from` never copies them, even inside a copied
	// folder.
	SecretFiles []string `toml:"secret_files"`
	// SecretEnvs are every env var the harness reads a login or key from.
	// They are removed from the user's environment on every run, so a key
	// exported in the shell can't override the profile's logins.
	SecretEnvs []string `toml:"secret_envs"`
	// Credentials maps each credential the harness can be given (an ID such
	// as "openai") to how it receives it. Being here means only that the
	// harness can take it: not that it's saved, allowed by a profile, or
	// still valid.
	Credentials map[string]Binding `toml:"credentials"`
	// ExclusiveCredentials are groups of credentials the harness must not
	// get together, because it would silently prefer one (e.g. Claude Code
	// uses an API key over a subscription token). A run gives at most one
	// per group: the first that the profile allows and agents has saved.
	ExclusiveCredentials [][]string `toml:"exclusive_credentials"`
}

// Binding is how a harness receives one credential: in an env var, or
// written into the harness's own auth file (e.g. pi's auth.json). Exactly
// one of Env and AuthFile is set.
type Binding struct {
	Env string `toml:"env"` // e.g. "OPENAI_API_KEY"; must also be in SecretEnvs

	AuthFile string `toml:"auth_file"` // the harness's auth file in its config folder, e.g. "auth.json"; must also be in SecretFiles
	Format   string `toml:"format"`    // the auth file's layout, e.g. "pi-auth-json"
	Key      string `toml:"key"`       // the credential's entry in the auth file, e.g. "openai-codex"
}

// validName is the rule for harness names; profile names follow the same rule.
var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// validEnvName matches env var names a shell would accept.
var validEnvName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Validate checks a Spec for mistakes that would make it unsafe or unusable,
// and reports them all at once.
func (s Spec) Validate() error {
	var problems []error
	if !validName.MatchString(s.Name) {
		problems = append(problems, errors.New("name: use lowercase letters, digits, - and _ (max 64)"))
	}
	if s.Binary == "" {
		problems = append(problems, errors.New("binary is required"))
	}
	if s.ConfigEnv == "" {
		problems = append(problems, errors.New("config_env is required"))
	}
	// Names with path parts could make --from copy from outside the folder.
	for _, f := range slices.Concat(s.SetupFiles, s.SecretFiles, slices.Sorted(maps.Keys(s.JSONListItems))) {
		switch {
		case strings.HasSuffix(f, "/"):
			problems = append(problems, fmt.Errorf("%q: drop the trailing / (folders don't need one)", f))
		case f == "" || f == "." || f == ".." || strings.ContainsAny(f, `/\`):
			problems = append(problems, fmt.Errorf("%q must be a single file or folder name", f))
		}
	}
	for _, f := range s.SetupFiles {
		if slices.Contains(s.SecretFiles, f) {
			problems = append(problems, fmt.Errorf("setup_files must not include login file %q", f))
		}
	}
	problems = append(problems, s.checkBindings()...)
	for _, group := range s.ExclusiveCredentials {
		if len(group) < 2 {
			problems = append(problems, fmt.Errorf("exclusive_credentials: group %v needs at least two credentials", group))
		}
		for _, id := range group {
			if _, ok := s.Credentials[id]; !ok {
				problems = append(problems, fmt.Errorf("exclusive_credentials: %q isn't in credentials", id))
			}
		}
	}
	if err := errors.Join(problems...); err != nil {
		return fmt.Errorf("harness %q:\n%w", s.Name, err)
	}
	return nil
}

// checkBindings reports problems with Credentials, for Validate.
func (s Spec) checkBindings() []error {
	supported := credential.Supported()
	var problems []error
	envOwner := map[string]string{} // env var -> the credential using it
	for _, id := range slices.Sorted(maps.Keys(s.Credentials)) {
		b := s.Credentials[id]
		c, isKnown := supported.Find(id)
		switch {
		case !isKnown:
			problems = append(problems, fmt.Errorf("credentials: unknown credential %q (supported: %s)", id, strings.Join(supported.IDs(), ", ")))
		case b.AuthFile != "" && !c.Renews():
			problems = append(problems, fmt.Errorf("credential %s can't go in an auth_file (only ones agents renews can); give it an env", id))
		}
		switch {
		case (b.Env == "") == (b.AuthFile == ""):
			problems = append(problems, fmt.Errorf("credential %s: set exactly one of env or auth_file", id))
		case b.Env != "":
			if b.Format != "" || b.Key != "" {
				problems = append(problems, fmt.Errorf("credential %s: format and key are only for auth_file", id))
			}
			switch {
			case !validEnvName.MatchString(b.Env):
				problems = append(problems, fmt.Errorf("credential %s: %q isn't a valid env var name", id, b.Env))
			case b.Env == s.ConfigEnv:
				problems = append(problems, fmt.Errorf("credential %s: %s is config_env", id, b.Env))
			case envOwner[b.Env] != "":
				// Otherwise which credential arrives would depend on map order.
				problems = append(problems, fmt.Errorf("credentials %s and %s both use %s", envOwner[b.Env], id, b.Env))
			}
			envOwner[b.Env] = id
			if !slices.Contains(s.SecretEnvs, b.Env) {
				// Otherwise a key exported in the shell would override the one agents gives.
				problems = append(problems, fmt.Errorf("credential %s's env var %s must also be in secret_envs", id, b.Env))
			}
		default:
			if !slices.Contains(s.SecretFiles, b.AuthFile) {
				// Otherwise --from could copy the credential into another profile.
				problems = append(problems, fmt.Errorf("credential %s's auth_file %q must also be in secret_files", id, b.AuthFile))
			}
			if formats := credential.Formats(); !slices.Contains(formats, b.Format) {
				problems = append(problems, fmt.Errorf("credential %s: unknown format %q (known: %s)", id, b.Format, strings.Join(formats, ", ")))
			}
			if b.Key == "" {
				problems = append(problems, fmt.Errorf("credential %s: key is required", id))
			}
		}
	}
	return problems
}

// Registry holds the known harnesses.
type Registry struct {
	specs map[string]Spec
}

// Load returns the built-in harnesses plus any *.toml files in dir. A user
// file may not reuse a built-in name.
func Load(dir string) (*Registry, error) {
	r := &Registry{specs: map[string]Spec{}}
	for _, s := range builtins() {
		r.specs[s.Name] = s
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.toml"))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		s, err := loadSpecFile(f)
		if err != nil {
			return nil, err
		}
		if _, taken := r.specs[s.Name]; taken {
			return nil, fmt.Errorf("%s: %q is already a harness", f, s.Name)
		}
		r.specs[s.Name] = s
	}
	return r, nil
}

// loadSpecFile reads and validates one user-defined harness. Unknown fields
// are an error, so a typo (e.g. "secret_fles") can't silently drop a setting.
func loadSpecFile(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, err
	}
	var s Spec
	dec := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		var unknown *toml.StrictMissingError
		if errors.As(err, &unknown) {
			return Spec{}, fmt.Errorf("%s: unknown field (check the spelling):\n%s", path, unknown.String())
		}
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return Spec{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Get returns the named harness.
func (r *Registry) Get(name string) (Spec, error) {
	s, ok := r.specs[name]
	if !ok {
		return Spec{}, fmt.Errorf("unknown harness %q (known: %s)", name, strings.Join(r.Names(), ", "))
	}
	return s, nil
}

// Names returns the harness names, sorted.
func (r *Registry) Names() []string {
	return slices.Sorted(maps.Keys(r.specs))
}

// Specs returns every harness, sorted by name.
func (r *Registry) Specs() []Spec {
	specs := make([]Spec, 0, len(r.specs))
	for _, name := range r.Names() {
		specs = append(specs, r.specs[name])
	}
	return specs
}
