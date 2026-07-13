// Package cli is the Rollops command surface, shared by both modes: in-process
// (one-shot, engine linked directly) and gRPC client (talking to a running
// daemon). Commands dispatch through the Operations seam so the surface is
// identical regardless of mode.
package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
	Approve(ctx context.Context, id string) (rollout.Rollout, error)
	Reject(ctx context.Context, id string) (rollout.Rollout, error)
	RollbackLast(ctx context.Context, targetRef string, force bool) (rollout.Rollout, error)
	Freeze(ctx context.Context, on bool, reason string) (bool, string, error)
}

// EngineOps adapts an in-process *engine.Engine to Operations, supplying the
// local actor identity for approve/reject (which the engine attributes to the
// approver). The gRPC client implements Operations directly (identity travels in
// the bearer token), so daemon-mode needs no adapter.
type EngineOps struct {
	*engine.Engine
	Actor rollout.Identity
}

// Approve approves a rollout, attributed to the local actor.
func (o EngineOps) Approve(ctx context.Context, id string) (rollout.Rollout, error) {
	return o.Engine.Approve(ctx, id, o.Actor)
}

// Reject rejects a rollout, attributed to the local actor.
func (o EngineOps) Reject(ctx context.Context, id string) (rollout.Rollout, error) {
	return o.Engine.Reject(ctx, id, o.Actor)
}

// Freeze toggles the emergency kill-switch, attributed to the local actor.
func (o EngineOps) Freeze(ctx context.Context, on bool, reason string) (bool, string, error) {
	return o.Engine.Freeze(ctx, on, o.Actor, reason)
}

type historyOperations interface {
	History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error)
}

// App is a configured CLI.
type App struct {
	Ops           Operations
	Out           io.Writer
	Actor         rollout.Identity // the invoking identity (one-shot inherits the local user)
	Doctor        Doctor
	PluginFetcher PluginFetcher                                                          // overrides plugin source retrieval (tests)
	CosignRun     func(ctx context.Context, name string, args ...string) (string, error) // overrides the cosign runner (tests)
	HTTPClient    *http.Client                                                           // for registry fetches (tests)
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
	NotifyChannels []string        // channel names for display (email, webhook)
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
	case "approve":
		return a.approve(ctx, rest)
	case "reject":
		return a.reject(ctx, rest)
	case "rollback":
		return a.rollback(ctx, rest)
	case "freeze":
		return a.freeze(ctx, rest, true)
	case "unfreeze":
		return a.freeze(ctx, rest, false)
	case "doctor":
		return a.doctor(ctx, rest)
	case "plugin":
		return a.plugin(ctx, rest)
	case "version", "--version":
		return a.version()
	case "help", "-h", "--help":
		return a.usage()
	default:
		return fmt.Errorf("unknown command %q (try: plan, apply, status, promote, approve, reject, rollback, freeze, unfreeze, doctor, plugin, version)", cmd)
	}
}

func (a *App) plan(ctx context.Context, args []string) error {
	c, root, err := loadConfigArg(args)
	if err != nil {
		return err
	}
	ctx = engine.WithRoot(ctx, root)
	p, err := a.Ops.Plan(ctx, c)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(a.Out, p.Summary)
	// Surface the rendered manifest for a referenced source so the resolved
	// result is reviewable before an apply.
	if len(p.Rendered) > 0 {
		_, _ = fmt.Fprintf(a.Out, "\n--- rendered manifest ---\n%s\n", p.Rendered)
	}
	return nil
}

func (a *App) apply(ctx context.Context, args []string) error {
	c, root, err := loadConfigArg(args)
	if err != nil {
		return err
	}
	ctx = engine.WithRoot(ctx, root)
	// One-shot apply produces a plan first so it satisfies plan-before-apply.
	if _, err := a.Ops.Plan(ctx, c); err != nil {
		return err
	}
	r, err := a.Ops.Apply(ctx, engine.ApplyRequest{Config: c, Root: root, Initiator: a.Actor, Planned: true})
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "rollout %s: %s (%s)\n", r.ID, r.Phase, r.TargetRef)
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
	_, _ = fmt.Fprintf(a.Out, "%s\t%s\t%s\t%s\n", r.ID, r.Phase, r.TargetRef, r.Strategy)
	if r.StepTotal > 0 {
		_, _ = fmt.Fprintf(a.Out, "steps\t%d/%d (%d%%)\n", r.StepIndex, r.StepTotal, r.StepWeight)
	}
	if h, ok := a.Ops.(historyOperations); ok {
		if hist, herr := h.History(ctx, r.TargetRef); herr == nil {
			for _, rec := range hist {
				if rec.RolloutID == r.ID && rec.Note != "" {
					_, _ = fmt.Fprintf(a.Out, "note\t%s\n", rec.Note)
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
	_, _ = fmt.Fprintf(a.Out, "rollout %s: %s\n", r.ID, r.Phase)
	return nil
}

func (a *App) approve(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("approve: rollout id required")
	}
	r, err := a.Ops.Approve(ctx, args[0])
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "rollout %s: %s\n", r.ID, r.Phase)
	return nil
}

func (a *App) reject(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("reject: rollout id required")
	}
	r, err := a.Ops.Reject(ctx, args[0])
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "rollout %s: %s\n", r.ID, r.Phase)
	return nil
}

// freeze engages (on) or lifts (off) the emergency kill-switch. For freeze, any
// args are joined into the reason.
func (a *App) freeze(ctx context.Context, args []string, on bool) error {
	reason := strings.Join(args, " ")
	active, r, err := a.Ops.Freeze(ctx, on, reason)
	if err != nil {
		return err
	}
	if active {
		_, _ = fmt.Fprintf(a.Out, "frozen: %s\n", r)
	} else {
		_, _ = fmt.Fprintln(a.Out, "unfrozen")
	}
	return nil
}

func (a *App) rollback(ctx context.Context, args []string) error {
	// --force overrides the backward-compatibility gate (non-backwardCompatible
	// migration with no reverse command). Accept it in any position.
	force := false
	var rest []string
	for _, arg := range args {
		if arg == "--force" || arg == "-f" {
			force = true
			continue
		}
		rest = append(rest, arg)
	}
	if len(rest) < 1 {
		return fmt.Errorf("rollback: target ref required")
	}
	r, err := a.Ops.RollbackLast(ctx, rest[0], force)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "rollout %s: %s (%s)\n", r.ID, r.Phase, r.TargetRef)
	return nil
}

func (a *App) doctor(ctx context.Context, args []string) error {
	var failed []string
	if len(args) > 0 {
		if _, _, err := loadConfigArg(args[:1]); err != nil {
			_, _ = fmt.Fprintf(a.Out, "config: fail (%v)\n", err)
			failed = append(failed, "config")
		} else {
			_, _ = fmt.Fprintf(a.Out, "config: ok (%s)\n", args[0])
		}
	} else {
		_, _ = fmt.Fprintln(a.Out, "config: skipped (pass rollops.yaml to validate)")
	}

	if a.Doctor.DaemonAddr != "" {
		if a.Doctor.Probe == nil {
			_, _ = fmt.Fprintln(a.Out, "daemon: fail (probe not configured)")
			failed = append(failed, "daemon")
		} else if err := a.Doctor.Probe(ctx, a.Doctor.DaemonAddr, a.Doctor.Token); err != nil {
			_, _ = fmt.Fprintf(a.Out, "daemon: fail (%v)\n", err)
			failed = append(failed, "daemon")
		} else {
			_, _ = fmt.Fprintf(a.Out, "daemon: ok (%s)\n", a.Doctor.DaemonAddr)
		}
	} else {
		dbPath := a.Doctor.DBPath
		if dbPath == "" {
			dbPath = "rollops.db"
		}
		db, err := sqlite.Open(dbPath)
		if err != nil {
			_, _ = fmt.Fprintf(a.Out, "database: fail (%v)\n", err)
			failed = append(failed, "database")
		} else {
			_ = db.Close()
			_, _ = fmt.Fprintf(a.Out, "database: ok (%s)\n", dbPath)
		}
	}

	if a.Doctor.Notifier != nil {
		if err := a.Doctor.Notifier.Notify(ctx, notify.Event{Kind: notify.Test, TargetRef: "doctor"}); err != nil {
			_, _ = fmt.Fprintf(a.Out, "notify: fail (%v)\n", err)
			failed = append(failed, "notify")
		} else {
			_, _ = fmt.Fprintf(a.Out, "notify: ok (%s)\n", strings.Join(a.Doctor.NotifyChannels, ", "))
		}
	} else {
		_, _ = fmt.Fprintln(a.Out, "notify: skipped (set ROLLOPS_BRIEFKASTEN_URL, ROLLOPS_SMTP_ADDR, or ROLLOPS_WEBHOOK_URL)")
	}

	if len(failed) > 0 {
		return fmt.Errorf("doctor failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

func (a *App) usage() error {
	_, _ = fmt.Fprintln(a.Out, "rollops <command> [args]\n\nCommands:\n  plan <config.yaml>       show what an apply would change\n  apply <config.yaml>      deploy desired state\n  status <rollout-id>      show a rollout's state\n  promote <rollout-id>     promote a verified rollout\n  approve <rollout-id>     approve a rollout awaiting approval\n  reject <rollout-id>      reject a rollout awaiting approval\n  rollback <target-ref>    roll target back to its previous desired state\n  freeze [reason]          engage the emergency kill-switch (block all applies)\n  unfreeze                 lift the emergency kill-switch\n  doctor [config.yaml]     check config, database, daemon, and notify readiness\n  plugin search [query]    search the plugin marketplace registry\n  plugin info <name>       show registry detail for a marketplace plugin\n  plugin install <src>     install a plugin by marketplace name, path, or https URL\n  plugin list              list installed plugins and their sha256 pins\n  plugin update [--apply]  check (or upgrade) installed plugins against the registry\n  version                  print build version")
	return nil
}

func (a *App) version() error {
	_, _ = fmt.Fprintln(a.Out, version.String())
	return nil
}

// loadConfigArg loads the config named by args[0] and returns it alongside the
// directory it lives in — the root that relative referenced manifest sources
// (manifestFrom) resolve against, so the one-shot CLI and the daemon behave
// identically.
func loadConfigArg(args []string) (*config.Config, string, error) {
	if len(args) < 1 {
		return nil, "", fmt.Errorf("config file path required")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return nil, "", fmt.Errorf("read config: %w", err)
	}
	c, err := config.Load(data)
	if err != nil {
		return nil, "", err
	}
	return c, filepath.Dir(args[0]), nil
}
