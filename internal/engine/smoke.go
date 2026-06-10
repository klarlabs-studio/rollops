package engine

import (
	"context"
	"os/exec"
)

// SmokeRunner runs a post-deploy smoke test command and reports its exit code.
// The observability-free auto-rollback contract: "run this, expect exit 0".
type SmokeRunner interface {
	Run(ctx context.Context, command []string) (exitCode int, err error)
}

// execSmoke runs the command locally via os/exec.
type execSmoke struct{}

func (execSmoke) Run(ctx context.Context, command []string) (int, error) {
	if len(command) == 0 {
		return 0, nil
	}
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), nil
	}
	return -1, err
}

type execDBRollback struct{}

func (execDBRollback) Run(ctx context.Context, command []string) error {
	if len(command) == 0 {
		return nil
	}
	return exec.CommandContext(ctx, command[0], command[1:]...).Run()
}
