// Package process runs programs for agents: it starts a harness and waits
// for it, passing signals and the exit code through as if the user had
// started it directly; checks whether a recorded process is still running;
// and opens web pages (OpenBrowser).
package process

import (
	"errors"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
)

// Plan is everything needed to start a harness.
type Plan struct {
	Binary string   // path to the program
	Args   []string // arguments after the program name
	Dir    string   // working folder
	Env    []string // complete environment, as KEY=value
}

// Environ returns environ without the variables named in drop, then with set
// added in sorted order (replacing any existing value).
func Environ(environ, drop []string, set map[string]string) []string {
	var env []string
	for _, kv := range environ {
		key, _, _ := strings.Cut(kv, "=")
		if _, replaced := set[key]; replaced || slices.Contains(drop, key) {
			continue
		}
		env = append(env, kv)
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		env = append(env, k+"="+set[k])
	}
	return env
}

// Child is a running harness.
type Child struct {
	cmd       *exec.Cmd
	forwarded chan os.Signal // read by forward until Wait
	// ignored is registered with signal.Notify but never read. Being
	// registered is what stops these signals from ending agents;
	// signal.Ignore can't be used because the harness would inherit it.
	ignored chan os.Signal
}

// Ctrl-C and Ctrl-\ already reach the harness from the terminal, so agents
// ignores them and lets the harness decide. Signals sent to agents alone
// (e.g. `kill`, or the terminal closing) are passed on.
var (
	ignored   = []os.Signal{syscall.SIGINT, syscall.SIGQUIT}
	forwarded = []os.Signal{syscall.SIGTERM, syscall.SIGHUP}
)

// Start starts the plan with the given standard streams. Until Wait
// returns, agents ignores Ctrl-C and forwards termination signals.
func Start(plan Plan, stdin io.Reader, stdout, stderr io.Writer) (*Child, error) {
	cmd := exec.Command(plan.Binary, plan.Args...)
	cmd.Dir, cmd.Env = plan.Dir, plan.Env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr

	// Separate channels, so a burst of ignored signals can't fill the buffer
	// and make Notify drop a signal that must be forwarded.
	c := &Child{cmd: cmd, forwarded: make(chan os.Signal, len(forwarded)), ignored: make(chan os.Signal, 1)}
	signal.Notify(c.forwarded, forwarded...)
	signal.Notify(c.ignored, ignored...)
	if err := cmd.Start(); err != nil {
		c.stopSignals()
		return nil, err
	}
	go forward(cmd.Process, c.forwarded)
	return c, nil
}

// forward passes signals to proc until signals is closed.
func forward(proc *os.Process, signals <-chan os.Signal) {
	for sig := range signals {
		_ = proc.Signal(sig) // it may have just exited
	}
}

// stopSignals restores normal signal handling for agents.
func (c *Child) stopSignals() {
	signal.Stop(c.forwarded)
	signal.Stop(c.ignored)
	close(c.forwarded)
}

// PID is the harness's process ID.
func (c *Child) PID() int { return c.cmd.Process.Pid }

// Wait waits for the harness to exit and returns its exit code. A harness
// killed by a signal gets 128 + the signal number, as in a shell. Call it
// (or Kill) only once: a second call fails, and closing the signal channel
// twice panics.
func (c *Child) Wait() (exitCode int, err error) {
	err = c.cmd.Wait()
	c.stopSignals()
	return exitCodeOf(err)
}

// exitCodeOf turns the result of exec.Cmd.Wait into an exit code.
func exitCodeOf(err error) (int, error) {
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			return 128 + int(status.Signal()), nil
		}
		return exit.ExitCode(), nil
	}
	return 0, err
}

// Kill stops the harness at once and waits for it to exit. Use it instead of
// Wait, not as well.
func (c *Child) Kill() {
	_ = c.cmd.Process.Kill()
	_, _ = c.Wait()
}
