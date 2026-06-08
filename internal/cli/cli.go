// Package cli is the Rolloffs command surface, shared by both modes: in-process
// (one-shot, engine linked directly) and gRPC client (talking to a running
// daemon). Commands dispatch through the Operations seam so the surface is
// identical regardless of mode.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
)

// Operations is the engine surface the CLI drives. Both the in-process adapter
// and the gRPC client implement it, keeping the command surface identical.
type Operations interface {
	Plan(ctx context.Context, c *config.Config) (*engine.Plan, error)
	Apply(ctx context.Context, req engine.ApplyRequest) (*rollout.Rollout, error)
	Status(ctx context.Context, id string) (rollout.Rollout, error)
	Promote(ctx context.Context, id string) (rollout.Rollout, error)
}

// App is a configured CLI.
type App struct {
	Ops   Operations
	Out   io.Writer
	Actor rollout.Identity // the invoking identity (one-shot inherits the local user)
}

// Run dispatches a command. Returns a non-nil error on failure; the caller maps
// that to an exit code.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return a.usage()
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "plan":
		return a.plan(ctx, rest)
	case "apply":
		return a.apply(ctx, rest)
	case "status":
		return a.status(ctx, rest)
	case "promote":
		return a.promote(ctx, rest)
	case "help", "-h", "--help":
		return a.usage()
	default:
		return fmt.Errorf("unknown command %q (try: plan, apply, status, promote)", cmd)
	}
}

func (a *App) plan(ctx context.Context, args []string) error {
	c, err := loadConfigArg(args)
	if err != nil {
		return err
	}
	p, err := a.Ops.Plan(ctx, c)
	if err != nil {
		return err
	}
	fmt.Fprintln(a.Out, p.Summary)
	return nil
}

func (a *App) apply(ctx context.Context, args []string) error {
	c, err := loadConfigArg(args)
	if err != nil {
		return err
	}
	// One-shot apply produces a plan first so it satisfies plan-before-apply.
	if _, err := a.Ops.Plan(ctx, c); err != nil {
		return err
	}
	r, err := a.Ops.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: a.Actor, Planned: true})
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "rollout %s: %s (%s)\n", r.ID, r.Phase, r.TargetRef)
	return nil
}

func (a *App) status(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("status: rollout id required")
	}
	r, err := a.Ops.Status(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", r.ID, r.Phase, r.TargetRef, r.Strategy)
	return nil
}

func (a *App) promote(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("promote: rollout id required")
	}
	r, err := a.Ops.Promote(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "rollout %s: %s\n", r.ID, r.Phase)
	return nil
}

func (a *App) usage() error {
	fmt.Fprintln(a.Out, "rolloffs <command> [args]\n\nCommands:\n  plan <config.yaml>     show what an apply would change\n  apply <config.yaml>    deploy desired state\n  status <rollout-id>    show a rollout's state\n  promote <rollout-id>   promote a verified rollout")
	return nil
}

func loadConfigArg(args []string) (*config.Config, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("config file path required")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return config.Load(data)
}
