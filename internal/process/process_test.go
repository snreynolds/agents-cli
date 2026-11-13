package process

import (
	"bytes"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestEnviron(t *testing.T) {
	got := Environ(
		[]string{"PATH=/bin", "OPENAI_API_KEY=sk-1", "PI_DIR=/old", "EMPTY=", "OPENAI_API_KEY=sk-2"},
		[]string{"OPENAI_API_KEY"},
		map[string]string{"PI_DIR": "/profile/config", "B": "2", "A": "1"},
	)
	want := []string{"PATH=/bin", "EMPTY=", "A=1", "B=2", "PI_DIR=/profile/config"}
	if !slices.Equal(got, want) {
		t.Errorf("Environ = %q, want %q", got, want)
	}
}

func shPlan(script string) Plan {
	return Plan{Binary: "/bin/sh", Args: []string{"-c", script}, Dir: os.TempDir(), Env: []string{"GREETING=hi"}}
}

func TestStart(t *testing.T) {
	var out bytes.Buffer
	p, err := Start(shPlan(`echo "$GREETING from $(pwd)"; exit 3`), nil, &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	code, err := p.Wait()
	if err != nil || code != 3 {
		t.Errorf("Wait = %d, %v; want 3", code, err)
	}
	if !strings.HasPrefix(out.String(), "hi from ") {
		t.Errorf("output %q: env or dir not passed", out.String())
	}
}

// A harness killed by a signal reports 128 + the signal, as a shell does.
func TestWait_killedBySignal(t *testing.T) {
	p, err := Start(shPlan(`kill -KILL $$`), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := p.Wait(); code != 128+int(syscall.SIGKILL) {
		t.Errorf("exit code %d, want %d", code, 128+int(syscall.SIGKILL))
	}
}

// trapScript is a harness that reports the signals it gets, and exits on
// SIGTERM or SIGHUP.
const trapScript = `trap 'echo INT' INT; trap 'echo QUIT' QUIT; trap 'echo TERM; exit 0' TERM; trap 'echo HUP; exit 0' HUP
echo ready; while :; do sleep 0.05; done`

// TestHelperAgents isn't a real test. TestStart_signals runs the test binary
// as a stand-in agents process that starts trapScript and waits for it.
func TestHelperAgents(t *testing.T) {
	if os.Getenv("AGENTS_TEST_HELPER") == "" {
		t.Skip("only runs as a helper process")
	}
	p, err := Start(shPlan(trapScript), nil, os.Stdout, os.Stderr)
	if err != nil {
		os.Exit(100)
	}
	code, _ := p.Wait()
	os.Exit(code)
}

// Signals sent to agents alone: SIGINT and SIGQUIT don't reach the harness
// (the terminal already delivers Ctrl-C to it), while SIGTERM and SIGHUP do,
// even straight after ignored ones (a burst once crowded them out).
func TestStart_signals(t *testing.T) {
	for _, c := range []struct {
		forwarded syscall.Signal
		name      string // as trapScript prints it
	}{
		{syscall.SIGTERM, "TERM"},
		{syscall.SIGHUP, "HUP"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for range 3 { // the bug this guards against showed up in most attempts
				var out syncBuffer
				agents := exec.Command(os.Args[0], "-test.run=^TestHelperAgents$")
				agents.Env = append(os.Environ(), "AGENTS_TEST_HELPER=1")
				agents.Stdout = &out
				// Its own process group, so a stuck run can be killed with the harness.
				agents.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
				if err := agents.Start(); err != nil {
					t.Fatal(err)
				}
				stuck := time.AfterFunc(5*time.Second, func() { _ = syscall.Kill(-agents.Process.Pid, syscall.SIGKILL) })
				waitFor(t, &out, "ready")
				_ = agents.Process.Signal(syscall.SIGINT)
				_ = agents.Process.Signal(syscall.SIGQUIT)
				_ = agents.Process.Signal(c.forwarded)
				err := agents.Wait()
				if !stuck.Stop() {
					t.Fatalf("the harness never got %s, so agents didn't exit; harness saw %q", c.name, out.String())
				}
				if err != nil {
					t.Fatalf("agents: %v; harness saw %q", err, out.String())
				}
				want := "ready\n" + c.name + "\n"
				if got := out.String(); got != want {
					t.Fatalf("harness saw %q, want %q", got, want)
				}
			}
		})
	}
}

func waitFor(t *testing.T, b *syncBuffer, s string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if strings.Contains(b.String(), s) {
			return
		}
	}
	t.Fatalf("timed out waiting for %q; got %q", s, b.String())
}

// syncBuffer is a bytes.Buffer that a test can read while a process writes.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
