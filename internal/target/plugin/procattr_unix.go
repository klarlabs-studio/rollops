//go:build unix

package plugin

import (
	"os/exec"
	"syscall"
)

// isolateProcess puts the plugin in its own process group so a single kill
// reaches forked grandchildren, not just the direct child.
func isolateProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the whole group led by pid (negative pid targets
// the group). Falls back to the single process when the group send fails.
func killProcessGroup(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
