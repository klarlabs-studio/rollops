//go:build unix

// Package procgroup runs child processes in their own process group so that
// cancelling one reaches the grandchildren it forked.
//
// This exists because os/exec does not. exec.CommandContext signals only the
// DIRECT child on cancellation, so a child that forks its own helper leaves that
// helper orphaned. The orphan reparents to PID 1, and when PID 1 is a Go binary
// with no init to reap it, it becomes a zombie that never leaves the process
// table.
//
// Live failure that produced this package: rollopsd ran `git fetch`, git forked
// `git-remote-https`, and each cancelled fetch stranded one helper. The leak ran
// at ~11 PIDs/min — tracking reconcile frequency — and reached the container's
// cgroup pids.max of 8050 in about twelve hours. After that EVERY reconcile
// failed with "cannot fork() for git-remote-https: Resource temporarily
// unavailable", no target in any watched project deployed, and the pod stayed
// 1/1 Ready throughout, so a wedged controller was indistinguishable from a
// healthy one.
package procgroup

import (
	"os/exec"
	"syscall"
)

// Isolate puts cmd in its own process group so a single kill reaches forked
// grandchildren, not just the direct child. Call before Start.
func Isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Kill SIGKILLs the whole group led by pid (a negative pid targets the group).
// Falls back to the single process when the group send fails, which is the
// correct degradation: killing the child alone is what the standard library
// already does.
func Kill(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}
