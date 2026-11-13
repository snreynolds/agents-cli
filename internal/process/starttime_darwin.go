package process

import (
	"errors"

	"golang.org/x/sys/unix"
)

// StartTime returns an OS-specific stamp of when the process with this ID
// started (here, in microseconds since 1970). The OS reuses IDs, so an ID alone can
// name a different process later; the ID and its start time together
// can't. Compare stamps only for equality: their units differ by OS.
func StartTime(pid int) (int64, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	// For an unused ID, sysctl returns no entry, which x/sys reports as EIO
	// (the result's size is wrong).
	if errors.Is(err, unix.EIO) || errors.Is(err, unix.ESRCH) || (err == nil && info.Proc.P_pid != int32(pid)) {
		return 0, errNotRunning
	}
	if err != nil {
		return 0, err
	}
	t := info.Proc.P_starttime
	return t.Sec*1_000_000 + int64(t.Usec), nil
}
