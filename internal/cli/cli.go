// Package cli is the Rollops command surface, shared by both modes: in-process
// (one-shot, engine linked directly) and gRPC client (talking to a running
// daemon). Commands dispatch through the Operations seam so the surface is
// identical regardless of mode.
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	"go.klarlabs.de/rollops/internal/version"
)

// Operations is the engine surface the CLI drives. Both the in-process adapter
// and the gRPC client implement it, keeping the command surface identical.
type Operations interface {
	Plan(ctx context.Context, c *config.Config) (*engine.Plan, error)
	Apply(ctx context.Context, req engine.ApplyRequest) (*rollout.Rollout, error)
	Status(ctx context.Context, id string) (rollout.Rollout, error)
	Promote(ctx context.Context, id string) (rollout.Rollout, error)
	RollbackLast(ctx context.Context, targetRef string) (rollout.Rollout, error)
}

type historyOperations interface {
	History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error)
}

// App is a configured CLI.
type App struct {
	Ops    Operations
	Out    io.Writer
	Actor  rollout.Identity // the invoking identity (one-shot inherits the local user)
	Doctor Doctor
}

// DaemonProbe checks whether a daemon can be reached and authenticated.
type DaemonProbe func(ctx context.Context, addr, token string) error

// Doctor configures the CLI's release-readiness diagnostics.
type Doctor struct {
	DBPath         string
	DaemonAddr     string
	Token          string
	Probe          DaemonProbe
	Notifier       notify.Notifier // when set, doctor sends a test event
	NotifyChannels []string        // channel names for display (telegram, webhook)
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
	case "rollback":
		return a.rollback(ctx, rest)
	case "doctor":
		return a.doctor(ctx, rest)
	case "version", "--version":
		return a.version()
	case "help", "-h", "--help":
		return a.usage()
	default:
		return fmt.Errorf("unknown command %q (try: plan, apply, status, promote, rollback, doctor, version)", cmd)
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
	if h, ok := a.Ops.(historyOperations); ok {
		if hist, herr := h.History(ctx, r.TargetRef); herr == nil {
			for _, rec := range hist {
				if rec.RolloutID == r.ID && rec.Note != "" {
					fmt.Fprintf(a.Out, "note\t%s\n", rec.Note)
					break
				}
			}
		}
	}
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

func (a *App) rollback(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("rollback: target ref required")
	}
	r, err := a.Ops.RollbackLast(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Fprintf(a.Out, "rollout %s: %s (%s)\n", r.ID, r.Phase, r.TargetRef)
	return nil
}

func (a *App) doctor(ctx context.Context, args []string) error {
	var failed []string
	if len(args) > 0 {
		if _, err := loadConfigArg(args[:1]); err != nil {
			fmt.Fprintf(a.Out, "config: fail (%v)\n", err)
			failed = append(failed, "config")
		} else {
			fmt.Fprintf(a.Out, "config: ok (%s)\n", args[0])
		}
	} else {
		fmt.Fprintln(a.Out, "config: skipped (pass rollops.yaml to validate)")
	}

	if a.Doctor.DaemonAddr != "" {
		if a.Doctor.Probe == nil {
			fmt.Fprintln(a.Out, "daemon: fail (probe not configured)")
			failed = append(failed, "daemon")
		} else if err := a.Doctor.Probe(ctx, a.Doctor.DaemonAddr, a.Doctor.Token); err != nil {
			fmt.Fprintf(a.Out, "daemon: fail (%v)\n", err)
			failed = append(failed, "daemon")
		} else {
			fmt.Fprintf(a.Out, "daemon: ok (%s)\n", a.Doctor.DaemonAddr)
		}
	} else {
		dbPath := a.Doctor.DBPath
		if dbPath == "" {
			dbPath = "rollops.db"
		}
		db, err := sqlite.Open(dbPath)
		if err != nil {
			fmt.Fprintf(a.Out, "database: fail (%v)\n", err)
			failed = append(failed, "database")
		} else {
			_ = db.Close()
			fmt.Fprintf(a.Out, "database: ok (%s)\n", dbPath)
		}
	}

	if a.Doctor.Notifier != nil {
		if err := a.Doctor.Notifier.Notify(ctx, notify.Event{Kind: notify.Test, TargetRef: "doctor"}); err != nil {
			fmt.Fprintf(a.Out, "notify: fail (%v)\n", err)
			failed = append(failed, "notify")
		} else {
			fmt.Fprintf(a.Out, "notify: ok (%s)\n", strings.Join(a.Doctor.NotifyChannels, ", "))
		}
	} else {
		fmt.Fprintln(a.Out, "notify: skipped (set ROLLOPS_TELEGRAM_TOKEN or ROLLOPS_WEBHOOK_URL)")
	}

	if len(failed) > 0 {
		return fmt.Errorf("doctor failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

func (a *App) usage() error {
	fmt.Fprintln(a.Out, "rollops <command> [args]\n\nCommands:\n  plan <config.yaml>       show what an apply would change\n  apply <config.yaml>      deploy desired state\n  status <rollout-id>      show a rollout's state\n  promote <rollout-id>     promote a verified rollout\n  rollback <target-ref>    roll target back to its previous desired state\n  doctor [config.yaml]     check config, database, daemon, and notify readiness\n  version                  print build version")
	return nil
}

func (a *App) version() error {
	fmt.Fprintln(a.Out, version.String())
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
