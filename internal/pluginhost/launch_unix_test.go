//go:build unix

package pluginhost

import (
	"context"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestLaunch_KillsProcessGroupOnHandshakeFailure verifies that when the
// handshake fails, Launch kills the whole process group — so a plugin that
// forked a child before failing does not leave an orphan. The probe plugin
// forks a `sleep`, records its pid, then emits a wrong-cookie handshake.
func TestLaunch_KillsProcessGroupOnHandshakeFailure(t *testing.T) {
	bin := probeBinary(t)
	pidfile := t.TempDir() + "/child.pid"
	t.Setenv("PROBE_FORK_PIDFILE", pidfile)

	_, err := Launch(context.Background(), bin, []string{"PROBE_FORK_PIDFILE"})
	if err == nil {
		t.Fatal("wrong-cookie handshake must fail Launch")
	}

	pid := readChildPID(t, pidfile)
	if pid <= 0 {
		t.Fatalf("probe plugin did not record a forked child pid (got %d)", pid)
	}
	if !waitProcessGone(pid, 3*time.Second) {
		t.Errorf("forked child pid %d survived handshake-failure teardown (orphan leak)", pid)
	}
}

func readChildPID(t *testing.T, path string) int {
	t.Helper()
	// The child pid is written just before the (failing) handshake; give the
	// file a brief window to appear.
	deadline := time.Now().Add(2 * time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			if pid, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
				return pid
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("child pidfile never became readable: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitProcessGone polls signal-0 until the process no longer exists or the
// timeout elapses.
func waitProcessGone(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err == syscall.ESRCH {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return syscall.Kill(pid, 0) == syscall.ESRCH
}
