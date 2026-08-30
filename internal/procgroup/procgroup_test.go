//go:build unix

package procgroup

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// A cancelled command must take its GRANDCHILDREN with it.
//
// This is the leak that wedged rollopsd every twelve hours. exec.CommandContext
// signals only the direct child, so `git` died and the `git-remote-https` it had
// forked survived, orphaned, and became a zombie on PID 1. Here a shell stands in
// for git and a long `sleep` for the transport helper.
//
// Without Isolate + a group-killing Cancel, the sleep outlives the shell and this
// test fails — which is exactly the production behaviour.
func TestCancellingAParentKillsTheGrandchild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Print the grandchild's pid, then hold the shell open.
	cmd := exec.CommandContext(ctx, "sh", "-c", "sleep 300 & echo $!; wait")
	Isolate(cmd)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			Kill(cmd.Process.Pid)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	var grandchild int
	if _, err := fscanPid(stdout, &grandchild); err != nil || grandchild <= 0 {
		t.Fatalf("could not read grandchild pid: %v", err)
	}
	if !alive(grandchild) {
		t.Fatalf("grandchild %d was never alive; the test proves nothing", grandchild)
	}

	cancel()
	_ = cmd.Wait()

	// Give the signal a moment to land.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(grandchild) {
			return // killed with its parent, as intended
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Don't strand it in the test runner.
	_ = syscall.Kill(grandchild, syscall.SIGKILL)
	t.Fatalf("grandchild %d SURVIVED cancellation of its parent — this is the orphan that "+
		"reparents to PID 1 and accumulates until the container cannot fork", grandchild)
}

// alive reports whether pid exists and has not been reaped. Signal 0 checks for
// existence without delivering anything.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }
