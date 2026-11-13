package process

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestStartTime(t *testing.T) {
	start, err := StartTime(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := StartTime(os.Getpid()); again != start {
		t.Errorf("start time changed: %d then %d", start, again)
	}
	if !Running(os.Getpid(), start) {
		t.Error("this process isn't running")
	}
}

func TestRunning_ended(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := StartTime(cmd.Process.Pid); !errors.Is(err, errNotRunning) {
		t.Errorf("finished process: got %v, want errNotRunning", err)
	}
	if Running(cmd.Process.Pid, 1) {
		t.Error("a finished process counts as running")
	}
}

// A recorded ID now held by a different process (same ID, other start time)
// isn't running. Doc scenario 27.
func TestRunning_reusedPID(t *testing.T) {
	start, _ := StartTime(os.Getpid())
	if Running(os.Getpid(), start-1) {
		t.Error("a reused PID counts as running")
	}
}

func TestParseStat(t *testing.T) {
	tail := " S 1 1 1 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 98765 1000 100"
	for name, c := range map[string]struct {
		stat string
		want int64 // -1 = expect an error
	}{
		"plain":            {"123 (pi)" + tail, 98765},
		"name with spaces": {"123 (my harness)" + tail, 98765},
		"name with ') '":   {"123 (a) S 9 9 (b)" + tail, 98765},
		"truncated":        {"123 (pi) S 1 1", -1},
		"no name":          {"garbage", -1},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseStat(c.stat)
			if c.want == -1 {
				if err == nil {
					t.Errorf("got %d, want an error", got)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("got %d, %v; want %d", got, err, c.want)
			}
		})
	}
}
