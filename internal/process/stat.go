package process

import (
	"errors"
	"strconv"
	"strings"
)

// parseStat returns the start time (field 22) from a Linux /proc/<pid>/stat
// line. It lives outside starttime_linux.go so its tests run on every OS.
func parseStat(stat string) (int64, error) {
	// Field 2, the command name, is in parentheses and may itself contain
	// ") ", so fields are counted from after the last ")" (field 3 onwards).
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, errors.New("/proc stat: no command name")
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 22-2 {
		return 0, errors.New("/proc stat: too few fields")
	}
	return strconv.ParseInt(fields[22-3], 10, 64)
}
