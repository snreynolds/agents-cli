package process

import "errors"

// errNotRunning is returned for a process ID that no process has.
var errNotRunning = errors.New("no such process")

// Running reports whether the process with this ID and start time is still
// running. If that can't be checked, it answers yes: wrongly treating a run
// as live only delays a cleanup, while wrongly treating it as ended could
// let its profile be deleted mid-run.
func Running(pid int, start int64) bool {
	now, err := StartTime(pid)
	if err != nil {
		return !errors.Is(err, errNotRunning)
	}
	return now == start
}
