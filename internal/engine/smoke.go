package engine

import (
	"context"
	"os"
	"os/exec"

	"go.klarlabs.de/rollops/internal/security"
)

// SmokeRunner runs a post-deploy smoke test command and reports its exit code.
// The observability-free auto-rollback contract: "run this, expect exit 0".
type SmokeRunner interface {
	Run(ctx context.Context, command []string) (exitCode int, err error)
}

// execSmoke runs the command locally via os/exec. It is the single exec
// chokepoint for config-sourced smoke commands: the confinement policy is
// consulted before any process is spawned, so a poisoned tenant repo cannot run
// an arbitrary command on the daemon host when an allowlist is configured.
type execSmoke struct {
	confinement security.Confinement
}

func (e execSmoke) Run(ctx context.Context, command []string) (int, error) {
	if len(command) == 0 {
		return 0, nil
	}
	if err := e.confinement.CheckCommand(command); err != nil {
		return -1, err
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	// The command comes from repo config, which is untrusted: confine its
	// environment so it cannot read the daemon's secrets out of its own env.
	cmd.Env = e.confinement.CommandEnv(os.Environ())
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, err
}

// execDBRollback runs a database migrate/rollback hook via os/exec. Like
// execSmoke it enforces the command allowlist before spawning a process.
type execDBRollback struct {
	confinement security.Confinement
}

func (e execDBRollback) Run(ctx context.Context, command []string) error {
	if len(command) == 0 {
		return nil
	}
	if err := e.confinement.CheckCommand(command); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Env = e.confinement.CommandEnv(os.Environ()) // untrusted config-sourced command
	return cmd.Run()
}
