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
	"os/exec"
	"path/filepath"
	"strings"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/governance"
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
	Promote(ctx context.Context, id string, force bool) (rollout.Rollout, error)
	Verify(ctx context.Context, id string) (engine.VerifyReport, error)
	Approve(ctx context.Context, id string) (rollout.Rollout, error)
	Reject(ctx context.Context, id string) (rollout.Rollout, error)
	Pause(ctx context.Context, id string) (rollout.Rollout, error)
	Resume(ctx context.Context, id string) (rollout.Rollout, error)
	Abort(ctx context.Context, id string) (rollout.Rollout, error)
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

// Promote promotes a rollout, attributed to the local actor. force overrides
// the post-deploy gates.
func (o EngineOps) Promote(ctx context.Context, id string, force bool) (rollout.Rollout, error) {
	return o.Engine.Promote(ctx, id, o.Actor, force)
}

// Approve approves a rollout, attributed to the local actor.
func (o EngineOps) Approve(ctx context.Context, id string) (rollout.Rollout, error) {
	return o.Engine.Approve(ctx, id, o.Actor)
}

// Reject rejects a rollout, attributed to the local actor.
func (o EngineOps) Reject(ctx context.Context, id string) (rollout.Rollout, error) {
	return o.Engine.Reject(ctx, id, o.Actor)
}

// Pause holds an in-flight canary, attributed to the local actor.
func (o EngineOps) Pause(ctx context.Context, id string) (rollout.Rollout, error) {
	return o.Engine.Pause(ctx, id, o.Actor)
}

// Resume continues an operator-paused canary, attributed to the local actor.
func (o EngineOps) Resume(ctx context.Context, id string) (rollout.Rollout, error) {
	return o.Engine.Resume(ctx, id, o.Actor)
}

// Abort stops an in-flight canary and rolls it back, attributed to the local actor.
func (o EngineOps) Abort(ctx context.Context, id string) (rollout.Rollout, error) {
	return o.Engine.Abort(ctx, id, o.Actor)
}

// Freeze toggles the emergency kill-switch, attributed to the local actor.
func (o EngineOps) Freeze(ctx context.Context, on bool, reason string) (bool, string, error) {
	return o.Engine.Freeze(ctx, on, o.Actor, reason)
}

type historyOperations interface {
	History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error)
}

type fleetOperations interface {
	FleetStatus(ctx context.Context, filter string) (engine.FleetReport, error)
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
	// Governor, when set, is the external governance provider. doctor asks it a
	// question it can safely answer to prove it is reachable — because this gate fails
	// closed, and an operator should learn it is unreachable from doctor rather than
	// from a refused deploy during an incident.
	Governor    governance.Provider
	GovernorURL string
	// ToolProbe reports a render tool's version (kubectl/helm), or an error when
	// it is missing/unusable. Injectable for tests; defaults to execToolProbe.
	ToolProbe ToolProbe
}

// ToolProbe returns a render tool's version line, or an error when the tool is
// absent or fails to report a version.
type ToolProbe func(ctx context.Context, name string, args ...string) (string, error)

// execToolProbe locates a render tool and reads its version, so `doctor` can
// confirm a Kubernetes target's kustomize/Helm rendering will work before a
// rollout depends on it.
func execToolProbe(ctx context.Context, name string, args ...string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH", name)
	}
	out, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	line := strings.TrimSpace(firstLine(string(out)))
	if err != nil {
		if line != "" {
			return "", fmt.Errorf("%s: %s", err, line)
		}
		return "", err
	}
	if line == "" {
		line = path
	}
	return line, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
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
	case "fleet":
		return a.fleet(ctx, rest)
	case "promote":
		return a.promote(ctx, rest)
	case "verify":
		return a.verify(ctx, rest)
	case "approve":
		return a.approve(ctx, rest)
	case "reject":
		return a.reject(ctx, rest)
	case "pause":
		return a.pause(ctx, rest)
	case "resume":
		return a.resume(ctx, rest)
	case "abort":
		return a.abort(ctx, rest)
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
		return fmt.Errorf("unknown command %q (try: plan, apply, status, fleet, promote, pause, resume, abort, approve, reject, rollback, freeze, unfreeze, doctor, plugin, version)", cmd)
	}
}

func (a *App) plan(ctx context.Context, args []string) error {
	docs, root, err := loadDocumentsArg(args)
	if err != nil {
		return err
	}
	ctx = engine.WithRoot(ctx, root)
	if len(docs) > 1 {
		_, _ = fmt.Fprintf(a.Out, "RolloutSet → %d targets\n", len(docs))
	}
	for _, d := range docs {
		p, err := a.Ops.Plan(ctx, d.Config)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(a.Out, p.Summary)
		if len(p.Rendered) > 0 {
			_, _ = fmt.Fprintf(a.Out, "\n--- rendered manifest (%s) ---\n%s\n", d.Config.Spec.Target.Ref, p.Rendered)
		}
	}
	return nil
}

func (a *App) apply(ctx context.Context, args []string) error {
	data, root, err := readConfigFileArg(args)
	if err != nil {
		return err
	}
	if err := config.RefuseApplyRolloutSet(data); err != nil {
		return err
	}
	c, err := config.Load(data)
	if err != nil {
		return err
	}
	ctx = engine.WithRoot(ctx, root)
	// One-shot apply produces a plan first so it satisfies plan-before-apply.
	if _, err := a.Ops.Plan(ctx, c); err != nil {
		return err
	}
	r, err := a.Ops.Apply(ctx, engine.ApplyRequest{Config: c, Root: root, Initiator: a.Actor, Planned: true, Risk: engine.RiskFromConfig(c)})
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

func (a *App) fleet(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("fleet: set name or prefix required (e.g. web or web@)")
	}
	f, ok := a.Ops.(fleetOperations)
	if !ok {
		return fmt.Errorf("fleet: not supported by this operations backend")
	}
	rep, err := f.FleetStatus(ctx, args[0])
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "%s: %d/%d promoted", rep.Name, rep.Promoted, rep.Total)
	if rep.Active > 0 {
		_, _ = fmt.Fprintf(a.Out, " (%d active)", rep.Active)
	}
	if rep.Degraded > 0 {
		_, _ = fmt.Fprintf(a.Out, " (%d degraded)", rep.Degraded)
	}
	if rep.Awaiting > 0 {
		_, _ = fmt.Fprintf(a.Out, " (%d awaiting)", rep.Awaiting)
	}
	_, _ = fmt.Fprintln(a.Out)
	for _, m := range rep.Members {
		_, _ = fmt.Fprintf(a.Out, "  %s\t%s\t%s\n", m.Target, m.Phase, m.ID)
	}
	return nil
}

func (a *App) promote(ctx context.Context, args []string) error {
	// --force overrides the post-deploy gates (health, smoke, metric analysis)
	// that promote otherwise enforces. Accept it in any position, like rollback.
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
		return fmt.Errorf("promote: rollout id required")
	}
	r, err := a.Ops.Promote(ctx, rest[0], force)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "rollout %s: %s\n", r.ID, r.Phase)
	return nil
}

// verify dry-runs the post-deploy gate and prints one line per gate. It exits
// non-zero when a gate fails, so it composes in a script:
//
//	rollops verify ro-123 && rollops promote ro-123
func (a *App) verify(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("verify: rollout id required")
	}
	rep, err := a.Ops.Verify(ctx, args[0])
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(a.Out, "rollout %s: %s\n", rep.RolloutID, rep.Phase)
	for _, g := range rep.Gates {
		if g.Detail != "" {
			_, _ = fmt.Fprintf(a.Out, "%s\t%s\t%s\n", g.Gate, g.Status, g.Detail)
			continue
		}
		_, _ = fmt.Fprintf(a.Out, "%s\t%s\n", g.Gate, g.Status)
	}
	if !rep.OK {
		// A failing gate is a verdict, not a crash: report it as the command's
		// exit status without a stack of wrapping.
		return fmt.Errorf("verify: %s", rep.Reason)
	}
	_, _ = fmt.Fprintln(a.Out, "verify: ok (nothing changed)")
	return nil
}

func (a *App) pause(ctx context.Context, args []string) error {
	return a.rolloutIDCmd(ctx, args, "pause", a.Ops.Pause)
}

func (a *App) resume(ctx context.Context, args []string) error {
	return a.rolloutIDCmd(ctx, args, "resume", a.Ops.Resume)
}

func (a *App) abort(ctx context.Context, args []string) error {
	return a.rolloutIDCmd(ctx, args, "abort", a.Ops.Abort)
}

func (a *App) rolloutIDCmd(ctx context.Context, args []string, name string, op func(context.Context, string) (rollout.Rollout, error)) error {
	if len(args) < 1 {
		return fmt.Errorf("%s: rollout id required", name)
	}
	r, err := op(ctx, args[0])
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
	var cfg *config.Config
	if len(args) > 0 {
		docs, _, err := loadDocumentsArg(args[:1])
		if err != nil {
			_, _ = fmt.Fprintf(a.Out, "config: fail (%v)\n", err)
			failed = append(failed, "config")
		} else {
			_, _ = fmt.Fprintf(a.Out, "config: ok (%s) — %d rollout(s)\n", args[0], len(docs))
			for _, d := range docs {
				if note := d.Config.HonestStrategy(); note != "" {
					_, _ = fmt.Fprintf(a.Out, "strategy %s: %s\n", d.Config.Spec.Target.Ref, note)
				}
			}
			if len(docs) > 0 {
				cfg = docs[0].Config // probe tools from first member
			}
		}
	} else {
		_, _ = fmt.Fprintln(a.Out, "config: skipped (pass rollops.yaml to validate)")
	}

	if path := os.Getenv("ROLLOPS_CLUSTERS"); path != "" {
		clusters, err := config.LoadClustersFile(path)
		if err != nil {
			_, _ = fmt.Fprintf(a.Out, "clusters: fail (%v)\n", err)
			failed = append(failed, "clusters")
		} else {
			_, _ = fmt.Fprintf(a.Out, "clusters: ok (%s) — %d cluster(s)\n", path, len(clusters))
		}
	} else {
		_, _ = fmt.Fprintln(a.Out, "clusters: skipped (set ROLLOPS_CLUSTERS for cluster generators)")
	}

	// Render-tool readiness: a Kubernetes target renders through kubectl (always)
	// and helm (only when the spec uses a Helm source). Probe just what the config
	// needs, so a kustomize-only user is not told to install helm.
	if cfg != nil && cfg.Spec.Target.Kind == "kubernetes" {
		failed = append(failed, a.probeRenderTools(ctx, cfg.Spec.Target.Spec)...)
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

	if a.Doctor.Governor != nil {
		// A probe rather than a real request: action "probe" tells a governor this is a
		// readiness check and not a deploy it should record. Its verdict is irrelevant —
		// a governor answering "no" is still working. Only an error means trouble, and
		// with this gate failing closed, an unreachable governor blocks every apply.
		_, err := a.Doctor.Governor.Evaluate(ctx, governance.Request{Action: "probe", TargetRef: "doctor"})
		if err != nil {
			_, _ = fmt.Fprintf(a.Out, "governance: fail (%v) — applies will be refused while this is unreachable\n", err)
			failed = append(failed, "governance")
		} else {
			_, _ = fmt.Fprintf(a.Out, "governance: ok (%s, fail-closed)\n", a.Doctor.GovernorURL)
		}
	} else {
		_, _ = fmt.Fprintln(a.Out, "governance: skipped (set ROLLOPS_GOVERNANCE_URL to require external approval)")
	}

	if len(failed) > 0 {
		return fmt.Errorf("doctor failed: %s", strings.Join(failed, ", "))
	}
	return nil
}

// probeRenderTools checks the render tools a Kubernetes target needs and prints
// an ok/fail line per tool, returning the names that failed. kubectl is always
// required (cluster apply/diff + kustomize); helm only when the spec renders a
// Helm chart.
func (a *App) probeRenderTools(ctx context.Context, spec map[string]any) []string {
	probe := a.Doctor.ToolProbe
	if probe == nil {
		probe = execToolProbe
	}
	var failed []string
	check := func(name string, args ...string) {
		ver, err := probe(ctx, name, args...)
		if err != nil {
			_, _ = fmt.Fprintf(a.Out, "%s: fail (%v)\n", name, err)
			failed = append(failed, name)
			return
		}
		_, _ = fmt.Fprintf(a.Out, "%s: ok (%s)\n", name, ver)
	}
	check("kubectl", "version", "--client")
	if specUsesHelm(spec) {
		check("helm", "version", "--short")
	}
	return failed
}

// specUsesHelm reports whether a Kubernetes target spec renders a Helm chart —
// via the flat `helm` key or a `manifestFrom.helm` referenced source.
func specUsesHelm(spec map[string]any) bool {
	if _, ok := spec["helm"]; ok {
		return true
	}
	if mf, ok := spec["manifestFrom"].(map[string]any); ok {
		if _, ok := mf["helm"]; ok {
			return true
		}
	}
	return false
}

func (a *App) usage() error {
	_, _ = fmt.Fprintln(a.Out, "rollops <command> [args]\n\nCommands:\n  plan <config.yaml>       show what an apply would change\n  apply <config.yaml>      deploy desired state\n  status <rollout-id>      show a rollout's state\n  fleet <name|prefix>     aggregate latest phases for a RolloutSet-style prefix\n  promote <rollout-id>     promote a rollout past its post-deploy gate (--force to override)\n  verify <rollout-id>      dry-run the post-deploy gate (changes nothing)\n  pause <rollout-id>       hold an in-flight canary at its current step\n  resume <rollout-id>      continue an operator-paused canary\n  abort <rollout-id>       stop an in-flight canary and roll it back\n  approve <rollout-id>     approve a rollout awaiting approval\n  reject <rollout-id>      reject a rollout awaiting approval\n  rollback <target-ref>    roll target back to its previous desired state\n  freeze [reason]          engage the emergency kill-switch (block all applies)\n  unfreeze                 lift the emergency kill-switch\n  doctor [config.yaml]     check config, database, daemon, and notify readiness\n  plugin search [query]    search the plugin marketplace registry\n  plugin info <name>       show registry detail for a marketplace plugin\n  plugin install <src>     install a plugin by marketplace name, path, or https URL\n  plugin list              list installed plugins and their sha256 pins\n  plugin update [--apply]  check (or upgrade) installed plugins against the registry\n  version                  print build version")
	return nil
}

func (a *App) version() error {
	_, _ = fmt.Fprintln(a.Out, version.String())
	return nil
}

func readConfigFileArg(args []string) ([]byte, string, error) {
	if len(args) < 1 {
		return nil, "", fmt.Errorf("config file path required")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return nil, "", fmt.Errorf("read config: %w", err)
	}
	return data, filepath.Dir(args[0]), nil
}

// loadDocumentsArg expands a RolloutConfig or RolloutSet the same way the
// watcher does, and loads ROLLOPS_CLUSTERS for cluster generators.
func loadDocumentsArg(args []string) ([]config.NamedConfig, string, error) {
	data, root, err := readConfigFileArg(args)
	if err != nil {
		return nil, "", err
	}
	if err := config.LoadClusterRegistryEnv(os.Getenv); err != nil {
		return nil, "", err
	}
	docs, err := config.LoadDocuments(data, args[0])
	if err != nil {
		return nil, "", err
	}
	return docs, root, nil
}
