package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeHarness is a stand-in harness: by default it prints what it was given;
// "exit N" exits with N; "wait" writes its process ID to config/ready, then
// runs until it gets SIGTERM.
const fakeHarness = `#!/bin/sh
case "$1" in
exit) exit "$2" ;;
wait) trap 'exit 0' TERM; echo $$ > "$FAKE_CONFIG_DIR/ready"; while :; do sleep 0.05; done ;;
esac
echo "config=$FAKE_CONFIG_DIR"
echo "key=${FAKE_API_KEY-unset} token=${FAKE_TOKEN-unset} openai=${OPENAI_API_KEY-unset} path=${PATH:+set}"
echo "dir=$(pwd -P)"
echo "args=$*"
`

// newFakeHome returns an agents home where harness "fake" runs the fake
// harness script (or binary, if given).
func newFakeHome(t *testing.T, binary string) string {
	t.Helper()
	home := t.TempDir()
	if binary == "" {
		binary = filepath.Join(home, "fake.sh")
		writeFile(t, binary, fakeHarness, 0o700)
	}
	writeFile(t, filepath.Join(home, "harnesses", "fake.toml"), fmt.Sprintf(`
name = "fake"
binary = %q
config_env = "FAKE_CONFIG_DIR"
secret_envs = ["FAKE_API_KEY", "FAKE_TOKEN"]
exclusive_credentials = [["claude", "openai"]]
[credentials]
openai = { env = "FAKE_API_KEY" }
claude = { env = "FAKE_TOKEN" }
`, binary), 0o600)
	return home
}

func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatal(err)
	}
}

// Each profile runs in the current folder with its own config folder, gets
// its arguments unchanged, and never sees its harness's secret env vars from
// the shell; other variables pass through. Doc scenarios 1 and 5 (host half).
func TestRun(t *testing.T) {
	home := newFakeHome(t, "")
	work := t.TempDir()
	t.Chdir(work)
	t.Setenv("FAKE_API_KEY", "secret")
	t.Setenv("OPENAI_API_KEY", "sk-other") // read by pi, not by the fake harness
	mustRun(t, home, "new", "a", "fake")
	mustRun(t, home, "new", "b", "fake")

	workDir, _ := filepath.EvalSymlinks(work)
	for _, c := range []struct{ profile, args, want string }{
		{"a", "", "config=" + filepath.Join(home, "profiles", "a", "config")},
		{"b", "", "config=" + filepath.Join(home, "profiles", "b", "config")},
		{"a", "", "key=unset token=unset openai=sk-other path=set"},
		{"a", "", "dir=" + workDir},
		{"a", "--continue -m x", "args=--continue -m x"},
		{"a", "-- --help", "args=--help"},
	} {
		t.Run(c.profile+" "+c.want, func(t *testing.T) {
			out := mustRun(t, home, append([]string{"run", c.profile}, strings.Fields(c.args)...)...)
			if !strings.Contains(out, c.want+"\n") {
				t.Errorf("harness output:\n%s\nwant line %q", out, c.want)
			}
		})
	}
}

func TestRun_exitCode(t *testing.T) {
	home := newFakeHome(t, "")
	mustRun(t, home, "new", "a", "fake")
	_, _, err := run(t, home, "", "run", "a", "exit", "7")
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Errorf("got %v, want exit code 7", err)
	}
}

func TestRun_refusals(t *testing.T) {
	home := newFakeHome(t, "/no/such/harness-binary")
	mustRun(t, home, "new", "plain", "fake")
	mustRun(t, home, "new", "broken", "fake")
	writeFile(t, filepath.Join(home, "profiles", "broken", "profile.toml"), "[broken", 0o600)
	for name, c := range map[string]struct{ profile, want string }{
		"not installed":   {"plain", "can't start fake (is it installed?)"},
		"broken profile":  {"broken", "profile broken"},
		"no such profile": {"nope", "profile not found"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := run(t, home, "", "run", c.profile); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want %q", err, c.want)
			}
		})
	}
}

// While a profile runs, ls shows it and rm refuses to delete it.
func TestRun_liveRun(t *testing.T) {
	home := newFakeHome(t, "")
	mustRun(t, home, "new", "a", "fake")
	pid, done := startWaiting(t, home, "a")
	if out := mustRun(t, home, "ls"); !strings.Contains(out, "RUNNING") || !strings.Contains(out, " 1\n") {
		t.Errorf("ls doesn't show the run:\n%s", out)
	}
	if _, _, err := run(t, home, "", "rm", "a", "--yes"); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Errorf("rm while running: got %v", err)
	}
	stopWaiting(t, pid, done)
	if n := listJSON(t, home)["a"].Running; n != 0 {
		t.Errorf("ls --json running = %d after exit, want 0", n)
	}
	mustRun(t, home, "rm", "a", "--yes")
}

// startWaiting runs the fake harness in "wait" mode for a profile and returns
// once the harness is ready and its run is recorded, with the harness's
// process ID and a channel that gets the run's result.
func startWaiting(t *testing.T, home, profile string) (pid int, done <-chan error) {
	t.Helper()
	result := make(chan error, 1) // buffered, so the goroutine can finish if the test fails first
	go func() {
		_, _, err := run(t, home, "", "run", profile, "wait")
		result <- err
	}()
	ready := filepath.Join(home, "profiles", profile, "config", "ready")
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("no recorded run with a ready harness (pid %d)", pid)
		}
		if data, err := os.ReadFile(ready); pid == 0 && err == nil && strings.HasSuffix(string(data), "\n") {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) }) // if the test fails before stopping it
		}
		if pid != 0 && listJSON(t, home)[profile].Running == 1 {
			return pid, result
		}
	}
}

// stopWaiting ends a harness started by startWaiting and checks its run ended
// cleanly.
func stopWaiting(t *testing.T, pid int, done <-chan error) {
	t.Helper()
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}
