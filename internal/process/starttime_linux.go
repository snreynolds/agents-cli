package process

import (
	"errors"
	"fmt"
	"os"
)

// StartTime returns an OS-specific stamp of when the process with this ID
// started (here, in clock ticks since boot). The OS reuses IDs, so an ID alone can
// name a different process later; the ID and its start time together
// can't. Compare stamps only for equality: their units differ by OS.
func StartTime(pid int) (int64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, os.ErrNotExist) {
		return 0, errNotRunning
	}
	if err != nil {
		return 0, err
	}
	return parseStat(string(data))
}
