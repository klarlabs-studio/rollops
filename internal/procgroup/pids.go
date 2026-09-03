package procgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultHeadroom is how many free PIDs must remain before /livez fails.
// Forking git + helpers needs spare slots; the v0.34.3 incident filled the
// cgroup completely, and readiness stayed green the whole time.
const DefaultHeadroom = 64

// PIDsStats is the container's process-slot usage from the cgroup controller.
type PIDsStats struct {
	Current int
	Max     int // 0 means unlimited ("max")
}

// ReadPIDs reads pids.current / pids.max from a cgroup filesystem root.
// Supports cgroup v2 (files at root) and v1 (files under pids/).
func ReadPIDs(cgroupRoot string) (PIDsStats, error) {
	current, max, err := readPair(cgroupRoot, "pids.current", "pids.max")
	if err == nil {
		return PIDsStats{Current: current, Max: max}, nil
	}
	current, max, err = readPair(filepath.Join(cgroupRoot, "pids"), "pids.current", "pids.max")
	if err != nil {
		return PIDsStats{}, err
	}
	return PIDsStats{Current: current, Max: max}, nil
}

func readPair(dir, curName, maxName string) (current, max int, err error) {
	curRaw, err := os.ReadFile(filepath.Join(dir, curName))
	if err != nil {
		return 0, 0, err
	}
	maxRaw, err := os.ReadFile(filepath.Join(dir, maxName))
	if err != nil {
		return 0, 0, err
	}
	current, err = strconv.Atoi(strings.TrimSpace(string(curRaw)))
	if err != nil {
		return 0, 0, fmt.Errorf("pids.current: %w", err)
	}
	max, err = parseMax(string(maxRaw))
	if err != nil {
		return 0, 0, err
	}
	return current, max, nil
}

func parseMax(raw string) (int, error) {
	s := strings.TrimSpace(raw)
	if s == "max" {
		return 0, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("pids.max: %w", err)
	}
	return n, nil
}

// UnderPressure reports whether current is within headroom of a finite max.
// Unlimited max (0) is never pressure. headroom <= 0 uses DefaultHeadroom.
func UnderPressure(s PIDsStats, headroom int) bool {
	if s.Max <= 0 {
		return false
	}
	if headroom <= 0 {
		headroom = DefaultHeadroom
	}
	return s.Current >= s.Max-headroom
}

// LivezStatus is the HTTP status and body for GET /livez against cgroupRoot.
// Missing/unreadable cgroups pass open — a probe that flaps when the host
// layout differs would be worse than the gap it closes. Pressure fails closed.
func LivezStatus(cgroupRoot string, headroom int) (code int, body string) {
	s, err := ReadPIDs(cgroupRoot)
	if err != nil {
		return 200, "ok"
	}
	if UnderPressure(s, headroom) {
		return 503, fmt.Sprintf("pids pressure: %d/%d (headroom %d)\n", s.Current, s.Max, effectiveHeadroom(headroom))
	}
	return 200, "ok"
}

func effectiveHeadroom(h int) int {
	if h <= 0 {
		return DefaultHeadroom
	}
	return h
}

// DefaultCgroupRoot is where Linux containers expose the unified or v1 pids
// controller. Overridable in tests via LivezStatus.
const DefaultCgroupRoot = "/sys/fs/cgroup"
