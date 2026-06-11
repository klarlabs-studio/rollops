//go:build unix

package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// TestClose_KillsProcessGroup verifies a child the plugin forked dies when the
// host tears the plugin down — the Setpgid + group-kill path.
func TestClose_KillsProcessGroup(t *testing.T) {
	bin := testPluginBinary(t)
	pidfile := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("ROLLOPS_TEST_CHILD_PIDFILE", pidfile)

	proc, err := Launch(context.Background(), bin)
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}

	// Wait for the plugin to record its forked child's pid.
	var childPid int
	for i := 0; i < 50; i++ {
		if b, err := os.ReadFile(pidfile); err == nil {
			if childPid, _ = strconv.Atoi(string(b)); childPid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if childPid == 0 {
		t.Fatal("plugin never recorded a child pid")
	}
	if syscall.Kill(childPid, 0) != nil {
		t.Fatalf("child %d should be alive before Close", childPid)
	}

	_ = proc.Close()

	// After group kill the child must be gone (signal 0 → ESRCH).
	dead := false
	for i := 0; i < 100; i++ {
		if syscall.Kill(childPid, 0) != nil {
			dead = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !dead {
		_ = syscall.Kill(childPid, syscall.SIGKILL) // cleanup if test failed
		t.Fatalf("forked child %d survived plugin teardown", childPid)
	}
}
