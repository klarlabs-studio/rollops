// Package engine is the central Go library of Rollops. Every interface — the
// one-shot CLI, the daemon (gRPC/REST), and the MCP server — is a thin client
// over this package, which is why the two control paths stay behaviourally
// identical. It is transport-agnostic and storage-agnostic: it depends only on
// the Store interface, the target Registry, and config.
//
// The engine exposes the seven operations of a rollout — plan, apply, verify,
// promote, rollback, observe, schedule. This file establishes the operation
// surface and core wiring; richer plan/diff (t-engine-plandiff) and per-target
// locking (t-engine-locks) layer on top, and statekit formalizes the lifecycle
// transitions this version drives directly.
package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"go.klarlabs.de/rollops/internal/analysis"
	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/depgraph"
	"go.klarlabs.de/rollops/internal/featureflags"
	"go.klarlabs.de/rollops/internal/governance"
	"go.klarlabs.de/rollops/internal/metricplugin"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/risk"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/secrets"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/step"
	"go.klarlabs.de/rollops/internal/store"
	itarget "go.klarlabs.de/rollops/internal/target"
	"go.klarlabs.de/rollops/internal/trafficrouting"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// Engine orchestrates rollouts over a Store and a target Registry.
type Engine struct {
	store    store.Store
	reg      *itarget.Registry
	locks    *keyedLocks
	policy   step.Policy
	smoke    SmokeRunner
	now      func() time.Time
	newID    func() string
	owner    string
	leaseTTL time.Duration

	// Optional trust/delivery collaborators. When set, Apply enforces them in
	// order; nil means that stage is skipped (keeps the bare engine simple for
	// tests, while a daemon wires the full pipeline).
	audit *audit.Logger

	// governance delegates the may-this-proceed decision to an external system. A
	// zero Hook holds no provider and allows everything, so an engine that was never
	// configured for it behaves exactly as before.
	governance   governance.Hook
	guardrails   *security.Guardrails
	artifact     *security.ArtifactGate
	secrets      secrets.Provider
	notifier     notify.Notifier
	metrics      analysis.MetricsProvider // optional override; else built from config
	analysis     bool
	confinement  security.Confinement // multi-tenant confinement policy (default off)
	dbRollback   DatabaseRollbackRunner
	flagBuild    func(context.Context, *config.FeatureFlags) (featureflags.Provider, error)   // flag plugin builder (test seam)
	routerBuild  func(context.Context, *config.TrafficRouting) (trafficrouting.Router, error) // traffic-router plugin builder (test seam)
	metricsBuild func(context.Context, *config.Analysis) (analysis.MetricsProvider, error)    // metric-provider plugin builder (test seam)
}

// Option configures an Engine.
type Option func(*Engine)

// WithClock overrides the time source (deterministic tests).
func WithClock(f func() time.Time) Option { return func(e *Engine) { e.now = f } }

// WithIDGen overrides the rollout id generator (deterministic tests).
func WithIDGen(f func() string) Option { return func(e *Engine) { e.newID = f } }

// WithLeaseOwner sets the owner id used for shared Store leases.
func WithLeaseOwner(owner string) Option { return func(e *Engine) { e.owner = owner } }

// WithLeaseTTL sets the TTL used for shared Store leases.
func WithLeaseTTL(ttl time.Duration) Option { return func(e *Engine) { e.leaseTTL = ttl } }

// WithPolicy overrides the fortify resilience policy applied to target ops.
func WithPolicy(p step.Policy) Option { return func(e *Engine) { e.policy = p } }

// WithSmokeRunner overrides the post-deploy smoke-test runner (tests).
func WithSmokeRunner(s SmokeRunner) Option { return func(e *Engine) { e.smoke = s } }

// WithConfinement installs the multi-tenant confinement policy. It also rebuilds
// the default exec-backed smoke and database runners so config-sourced commands
// are enforced against the command allowlist. Apply this before any runner
// override (WithSmokeRunner / WithDatabaseRollbackRunner) that must stay in
// effect for tests. Every control is opt-in; a zero-value Confinement is a no-op.
func WithConfinement(c security.Confinement) Option {
	return func(e *Engine) {
		e.confinement = c
		e.smoke = execSmoke{confinement: c}
		e.dbRollback = execDBRollback{confinement: c}
	}
}

// DatabaseRollbackRunner executes the optional rollback.database hook.
type DatabaseRollbackRunner interface {
	Run(ctx context.Context, command []string) error
}

// WithDatabaseRollbackRunner overrides the database rollback hook runner.
func WithDatabaseRollbackRunner(r DatabaseRollbackRunner) Option {
	return func(e *Engine) { e.dbRollback = r }
}

// WithAudit enables audit logging of the deploy pipeline.
func WithAudit(a *audit.Logger) Option { return func(e *Engine) { e.audit = a } }

// WithGuardrails enables the agent guardrails (freeze/rate-limit/policy floor).
func WithGuardrails(g *security.Guardrails) Option { return func(e *Engine) { e.guardrails = g } }

// WithGovernance delegates the may-this-proceed decision to an external provider.
//
// A nil provider is a no-op rather than an error, so a caller can pass
// governance.FromEnv(os.Getenv) unconditionally and get a gate only when the
// operator configured one.
func WithGovernance(p governance.Provider) Option {
	return func(e *Engine) { e.governance = governance.Hook{Provider: p} }
}

// governanceEnvironment reports which environment a rollout is heading for.
//
// The rollout-time input wins over the config's declared env: a config may be
// applied to more than one environment, and the caller performing the apply knows
// which one this is. Falling back to the declared env keeps the field populated for
// callers that do not supply risk inputs at all.
func governanceEnvironment(cfg *config.Config, req ApplyRequest) string {
	if req.Risk.Environment != "" {
		return req.Risk.Environment
	}
	return cfg.Spec.Target.Env
}

// WithArtifactGate enables artifact provenance verification before deploy.
func WithArtifactGate(g security.ArtifactGate) Option { return func(e *Engine) { e.artifact = &g } }

// WithSecrets enables resolution of "secret:<ref>" values in a target spec
// through the SecretProvider at execution time.
func WithSecrets(p secrets.Provider) Option { return func(e *Engine) { e.secrets = p } }

// WithNotifier enables operator notifications (approvals, failures, rollbacks,
// promotions). Best-effort: a failing notifier never blocks a rollout.
func WithNotifier(n notify.Notifier) Option { return func(e *Engine) { e.notifier = n } }

// WithMetricsProvider enables metric analysis and overrides the metrics backend
// used for analysis.
func WithMetricsProvider(p analysis.MetricsProvider) Option {
	return func(e *Engine) {
		e.metrics = p
		e.analysis = true
	}
}

// WithMetricAnalysis enables metric analysis. It is off by default so the base
// rollback path remains observability-free.
func WithMetricAnalysis() Option { return func(e *Engine) { e.analysis = true } }

// WithFlagProviderBuilder overrides how a feature-flag provider is built from
// config (test seam; default launches the configured flag plugin).
func WithFlagProviderBuilder(f func(context.Context, *config.FeatureFlags) (featureflags.Provider, error)) Option {
	return func(e *Engine) { e.flagBuild = f }
}

// WithTrafficRouterBuilder overrides how a traffic router is built from config
// (test seam; default launches the configured trafficrouter plugin).
func WithTrafficRouterBuilder(f func(context.Context, *config.TrafficRouting) (trafficrouting.Router, error)) Option {
	return func(e *Engine) { e.routerBuild = f }
}

// WithMetricsProviderBuilder overrides how a metricprovider plugin is built from
// an analysis config (test seam; default launches the configured plugin).
func WithMetricsProviderBuilder(f func(context.Context, *config.Analysis) (analysis.MetricsProvider, error)) Option {
	return func(e *Engine) { e.metricsBuild = f }
}

// driveTraffic shifts the configured percentage of traffic to the canary backend
// through a freshly launched traffic-router plugin, then closes it. A routing
// failure aborts the current step — a canary that did not shift traffic must
// not bake and promote. Callers that must not fail (rollback reset) ignore the
// error. Feature-flag apply stays best-effort; it is a different failure domain.
func (e *Engine) driveTraffic(ctx context.Context, ref string, tr *config.TrafficRouting, weight int) error {
	if tr == nil {
		return nil
	}
	router, err := e.routerBuild(ctx, tr)
	if err != nil {
		e.record(audit.Entry{Action: audit.ActionApply, TargetRef: ref, Detail: "trafficrouter build failed: " + err.Error()})
		return fmt.Errorf("trafficrouter build: %w", err)
	}
	if c, ok := router.(interface{ Close() error }); ok {
		defer func() { _ = c.Close() }()
	}
	hook := trafficrouting.Hook{Router: router}
	if err := hook.Apply(ctx, trafficrouting.Change{
		Route: tr.Route, Namespace: tr.Namespace,
		StableService: tr.StableService, CanaryService: tr.CanaryService, Weight: weight,
	}); err != nil {
		e.record(audit.Entry{Action: audit.ActionApply, TargetRef: ref, Detail: "trafficrouter apply failed: " + err.Error()})
		return fmt.Errorf("trafficrouter apply: %w", err)
	}
	e.record(audit.Entry{Action: audit.ActionApply, TargetRef: ref, Detail: fmt.Sprintf("trafficrouter %q → canary %d%%", tr.Route, weight)})
	return nil
}

// flagsEnabled reports whether feature-flag coupling fires for the given phase
// ("step" or "promote"), per the config's `when` (default both).
func flagsEnabled(ff *config.FeatureFlags, phase string) bool {
	if ff == nil {
		return false
	}
	switch ff.When {
	case "", "both":
		return true
	default:
		return ff.When == phase
	}
}

// driveFlag sets the configured flag's rollout percentage through a freshly
// launched flag provider, then closes it. Best-effort: a flag failure is
// logged via audit and never aborts the rollout.
func (e *Engine) driveFlag(ctx context.Context, ref string, ff *config.FeatureFlags, percentage int) {
	prov, err := e.flagBuild(ctx, ff)
	if err != nil {
		e.record(audit.Entry{Action: audit.ActionApply, TargetRef: ref, Detail: "featureflag build failed: " + err.Error()})
		return
	}
	if c, ok := prov.(interface{ Close() error }); ok {
		defer func() { _ = c.Close() }()
	}
	hook := featureflags.Hook{Provider: prov}
	if err := hook.Apply(ctx, featureflags.Change{Flag: ff.Flag, Environment: ff.Environment, Percentage: percentage}); err != nil {
		e.record(audit.Entry{Action: audit.ActionApply, TargetRef: ref, Detail: "featureflag apply failed: " + err.Error()})
		return
	}
	e.record(audit.Entry{Action: audit.ActionApply, TargetRef: ref, Detail: fmt.Sprintf("featureflag %q → %d%%", ff.Flag, percentage)})
}

// notifyEvent delivers an event best-effort.
func (e *Engine) notifyEvent(ctx context.Context, ev notify.Event) {
	if e.notifier != nil {
		_ = e.notifier.Notify(ctx, ev)
	}
}

// closeTarget releases a target's runtime resources (a plugin subprocess);
// no-op for plain targets. Engine operations defer this after every build.
func closeTarget(t pt.Target) {
	if c, ok := t.(io.Closer); ok {
		_ = c.Close()
	}
}

// build resolves a target by kind and wraps it in the fortify resilience
// envelope so every engine-driven target operation is retried/circuit-broken.
func (e *Engine) build(t config.Target) (pt.Target, error) {
	inner, err := e.reg.Build(t)
	if err != nil {
		return nil, err
	}
	return step.Wrap(inner, e.policy), nil
}

// New builds an Engine. By default ids are time-derived and the clock is
// time.Now; both are injectable for tests.
func New(st store.Store, reg *itarget.Registry, opts ...Option) *Engine {
	e := &Engine{
		store:        st,
		reg:          reg,
		locks:        newKeyedLocks(),
		policy:       step.DefaultPolicy(),
		smoke:        execSmoke{},
		dbRollback:   execDBRollback{},
		owner:        defaultOwner(),
		leaseTTL:     2 * time.Minute,
		flagBuild:    featureflags.BuildProvider,
		routerBuild:  trafficrouting.BuildRouter,
		metricsBuild: metricplugin.Build,
		now:          func() time.Time { return time.Now().UTC() },
	}
	e.newID = func() string { return "ro-" + e.now().Format("20060102T150405.000000000") }
	for _, o := range opts {
		o(e)
	}
	return e
}

func defaultOwner() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("%s:%d", host, os.Getpid())
}

// Plan computes what an apply would change without applying it: the desired
// manifest, the target's current fingerprint, and whether they differ. Drift =
// current fingerprint != desired checksum.
// Preflight reports whether every target in cs would apply, without applying
// any of them. A nil error means none of them objected.
//
// It renders each target exactly as Plan does, because a config that cannot
// render or whose target cannot be constructed is a failure the batch must
// hear about before it starts applying the others.
//
// The target is built WITHOUT the fortify wrapper (BuildTarget, same path as
// Diff/Resources/Reap). Preflighter is an optional capability on the concrete
// target; step.Guarded only forwards Apply/Observe/Health, so wrapping would
// silently drop the capability and the batch gate would never fire (#185).
//
// Targets that do not implement pt.Preflighter are skipped rather than assumed
// good: they contribute nothing to the decision, which is precisely the
// behaviour that existed before this method.
func (e *Engine) Preflight(ctx context.Context, cs []*config.Config) []error {
	var out []error
	for _, c := range cs {
		m, err := manifestFromConfig(c, rootFromContext(ctx))
		if err != nil {
			out = append(out, fmt.Errorf("%s: render: %w", c.Spec.Target.Ref, err))
			continue
		}
		if _, err := e.stampReferencedChecksum(ctx, c.Spec.Target.Ref, &m); err != nil {
			out = append(out, fmt.Errorf("%s: render: %w", c.Spec.Target.Ref, err))
			continue
		}
		tgt, err := e.BuildTarget(c.Spec.Target)
		if err != nil {
			out = append(out, fmt.Errorf("%s: build target: %w", c.Spec.Target.Ref, err))
			continue
		}
		pf, ok := tgt.(pt.Preflighter)
		if !ok {
			closeTarget(tgt)
			continue
		}
		err = pf.Preflight(ctx, m)
		closeTarget(tgt)
		if err != nil {
			out = append(out, fmt.Errorf("%s: %w", c.Spec.Target.Ref, err))
		}
	}
	return out
}

func (e *Engine) Plan(ctx context.Context, c *config.Config) (*Plan, error) {
	m, err := manifestFromConfig(c, rootFromContext(ctx))
	if err != nil {
		return nil, err
	}
	// Referenced sources (manifestFrom) key drift off the rendered output, so an
	// edit to a referenced file is detected even under shallow verification. The
	// rendered bytes double as the plan preview.
	rendered, err := e.stampReferencedChecksum(ctx, c.Spec.Target.Ref, &m)
	if err != nil {
		return nil, err
	}
	tgt, err := e.build(c.Spec.Target)
	if err != nil {
		return nil, err
	}
	defer closeTarget(tgt)
	cur, err := tgt.Observe(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: plan: observe: %w", err)
	}
	// Persist the observed fingerprint so drift status is queryable without
	// re-observing every target on a dashboard render.
	_ = e.store.SaveObservedState(ctx, rollout.TargetState{
		TargetRef:  c.Spec.Target.Ref,
		Observed:   cur,
		ObservedAt: e.now(),
	})
	// Verification depth (the stamped checksum is a shallow marker — an
	// out-of-band field edit like `kubectl set image` leaves it intact):
	//   shallow          — checksum stamp only (cheapest; no live diff).
	//   detect (default) — when the stamp says "in sync", live-diff anyway and
	//                      ALERT on any divergence, but do NOT auto-correct.
	//   full             — same live diff, and auto-reconcile drift to desired.
	// The live diff only runs when the cheap stamp already matches, so it is
	// bounded to one diff per in-sync target per tick.
	ver := c.Spec.Verification
	if ver == "" {
		ver = "detect"
	}
	deepDrift := false  // drift that triggers auto-reconcile (full)
	driftAlert := false // drift detected but intentionally not auto-corrected (detect)
	if (ver == "detect" || ver == "full") && cur.Value == m.Checksum && m.Checksum != "" {
		// Differ lives on the raw (unwrapped) target, not the fortify wrapper.
		raw, rerr := e.rawTarget(c.Spec.Target.Ref, m)
		if rerr == nil {
			defer closeTarget(raw)
			if d, ok := raw.(pt.Differ); ok {
				diff, derr := d.Diff(ctx, m)
				switch {
				case derr != nil:
					// Under full verification a diff we cannot compute is
					// indistinguishable from drift: fail CLOSED so reconcile
					// re-applies and self-heals rather than silently reporting
					// "in sync". Detect mode stays alert-only (no auto-correct).
					if ver == "full" {
						deepDrift = true
					}
				case strings.TrimSpace(diff) != "":
					if ver == "full" {
						deepDrift = true
					} else {
						driftAlert = true
					}
				}
			}
		} else if ver == "full" {
			// The target build for the live diff failed; treat as drift under full
			// verification so a build error cannot masquerade as "in sync".
			deepDrift = true
		}
	}
	p := newPlan(c.Spec.Target.Ref, m, cur, deepDrift)
	p.Rendered = rendered
	p.StrategyNote = c.HonestStrategy()
	p.Summary = p.render()
	if driftAlert {
		p.DriftAlert = true
		p.Summary = p.render()
	}
	// Surface a pending database migration so operators see it before applying.
	if mig := c.Spec.DatabaseMigrate(); mig != nil {
		p.Migration = fmt.Sprintf("migrate (%s): %s", c.Spec.DatabaseMigrateWhen(), strings.Join(mig.Command, " "))
		p.Summary = p.render()
	}
	// Attach the risk decision when configured so agents can escalate from
	// plan — not only discover a gate on apply.
	if c.Spec.Risk.Threshold > 0 || c.Spec.Risk.Sensitive != "" {
		d, err := e.EvaluateRisk(ctx, c, RiskFromConfig(c))
		if err != nil {
			return nil, err
		}
		p.RiskScore = d.Score
		p.NeedsApproval = d.NeedsApproval
		p.Sensitive = d.Sensitive
		p.RecentFailures = d.RecentFailures
		p.RiskReason = d.Reason
	}
	return p, nil
}

// DriftItem reports the drift status of one target.
type DriftItem struct {
	TargetRef string
	Phase     rollout.Phase
	Desired   string // desired checksum
	Observed  string // last observed fingerprint
	Drifted   bool
}

// DriftReport compares the last observed fingerprint of each target against its
// most recent rollout's desired checksum. Drift is asserted for the two
// settled phases whose desired state is authoritative: promoted, and
// rolled-back (rollback persists the restored manifest as Desired, so the
// rolled-back-to state is the baseline). In-flight or rejected rollouts make
// no claim about live state.
func (e *Engine) DriftReport(ctx context.Context) ([]DriftItem, error) {
	rs, err := e.store.ListRollouts(ctx, 0)
	if err != nil {
		return nil, err
	}
	fps, err := e.store.ObservedFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []DriftItem
	for _, r := range rs { // newest first
		if seen[r.TargetRef] {
			continue
		}
		seen[r.TargetRef] = true
		observed := fps[r.TargetRef]
		out = append(out, DriftItem{
			TargetRef: r.TargetRef,
			Phase:     r.Phase,
			Desired:   r.Desired.Checksum,
			Observed:  observed,
			Drifted:   r.Phase.Settled() && observed != r.Desired.Checksum,
		})
	}
	return out, nil
}

// PlanAction is the high-level effect an apply would have.
type PlanAction string

const (
	PlanCreate PlanAction = "create" // target has no observed state yet
	PlanUpdate PlanAction = "update" // observed state differs from desired
	PlanNoop   PlanAction = "noop"   // already at desired state
)

// Plan is the result of Plan — surfaced to humans and agents before apply. It
// is the "show exactly what will change before anything is applied" contract:
// an agent-driven Apply requires a Plan to have been produced first.
type Plan struct {
	TargetRef  string
	Desired    pt.Manifest
	Current    pt.Fingerprint
	Changed    bool
	Action     PlanAction
	Summary    string
	DeepDrift  bool   // full-verification diff found live ≠ desired despite a matching stamp
	DriftAlert bool   // detect-verification found live drift, but it is NOT auto-corrected (alert only)
	Migration  string // pending database migration ("migrate (when): cmd"), empty when none
	Rendered   []byte // rendered manifest when resolved from a referenced source (manifestFrom); nil otherwise
	// StrategyNote is the honest delivery name when strategy.type overclaims:
	// canary without trafficRouting/featureFlags is a health-gated bake;
	// blue-green without traffic routing is a full cutover. Empty when the
	// type name already matches what will run.
	StrategyNote string
	// Risk fields are set when spec.risk is configured (threshold > 0 or a
	// sensitive expression). Zero/false when risk is unconfigured so agents
	// can tell "no gate" from "low score".
	RiskScore     float64
	NeedsApproval bool
	Sensitive     bool
	// RecentFailures is the rolled-back count inside risk.history lookback
	// (0 when history is not configured). RiskReason is the gate's citeable
	// explanation for agents.
	RecentFailures int
	RiskReason     string
}

func newPlan(ref string, desired pt.Manifest, current pt.Fingerprint, deepDrift bool) *Plan {
	changed := current.Value != desired.Checksum || deepDrift
	action := PlanNoop
	switch {
	case current.Value == "":
		action = PlanCreate
	case changed:
		action = PlanUpdate
	}
	p := &Plan{TargetRef: ref, Desired: desired, Current: current, Changed: changed, Action: action, DeepDrift: deepDrift}
	p.Summary = p.render()
	return p
}

func (p *Plan) render() string {
	var base string
	if p.DriftAlert && p.Action == PlanNoop {
		base = fmt.Sprintf("%s [%s]: live drift detected — NOT auto-corrected (detect mode; set verification: full to self-heal) [checksum %s]", p.TargetRef, p.Desired.Kind, short(p.Desired.Checksum))
		if p.Migration != "" {
			base += "\n  + database " + p.Migration
		}
		if p.StrategyNote != "" {
			base += "\n  strategy: " + p.StrategyNote
		}
		return base
	}
	switch p.Action {
	case PlanNoop:
		base = fmt.Sprintf("%s [%s]: no changes (checksum %s)", p.TargetRef, p.Desired.Kind, short(p.Desired.Checksum))
	case PlanCreate:
		base = fmt.Sprintf("%s [%s]: create — deploy checksum %s (no current state observed)", p.TargetRef, p.Desired.Kind, short(p.Desired.Checksum))
	default:
		if p.DeepDrift {
			base = fmt.Sprintf("%s [%s]: update — live drifted from desired %s (full verification)", p.TargetRef, p.Desired.Kind, short(p.Desired.Checksum))
		} else {
			base = fmt.Sprintf("%s [%s]: update — %s → %s", p.TargetRef, p.Desired.Kind, short(p.Current.Value), short(p.Desired.Checksum))
		}
	}
	if p.Migration != "" {
		base += "\n  + database " + p.Migration
	}
	if p.StrategyNote != "" {
		base += "\n  strategy: " + p.StrategyNote
	}
	return base
}

// String renders the plan for CLI/agent display.
func (p *Plan) String() string { return p.Summary }

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

// Validation is the result of the validating phase: a confirmed config, the
// resolved deploy order, the blast radius of the target, and the plan/diff.
type Validation struct {
	Plan        *Plan
	BlastRadius int
	DeployOrder [][]string // topological layers (independents parallel, chains serialized)
}

// Validate runs the validating phase before any apply: it re-checks the config,
// resolves the dependency DAG (rejecting cycles), computes the blast radius, and
// produces the plan/diff. An agent-driven Apply requires a Validation to have
// been produced (its Plan satisfies the plan-before-apply guard).
func (e *Engine) Validate(ctx context.Context, c *config.Config, deps []rollout.Dependency) (*Validation, error) {
	if err := config.Validate(c); err != nil {
		return nil, err
	}
	nodes := []string{c.Spec.Target.Ref}
	g := depgraph.New(nodes, deps)
	order, err := g.Layers()
	if err != nil {
		return nil, fmt.Errorf("engine: validate: %w", err)
	}
	plan, err := e.Plan(ctx, c)
	if err != nil {
		return nil, err
	}
	return &Validation{
		Plan:        plan,
		BlastRadius: g.BlastRadius(c.Spec.Target.Ref),
		DeployOrder: order,
	}, nil
}

// RiskInputs are the rollout-time signals the engine cannot derive from config
// alone: the kind of change and the deploy environment, plus the blast radius
// (count of downstream dependents from the dependency graph).
type RiskInputs struct {
	ChangeType  string // config | code | schema
	Environment string // dev | staging | prod
	BlastRadius int
}

// RiskFromConfig fills the signals Apply cannot invent: environment from
// spec.target.env, change-type schema when a database migrate hook is present
// otherwise config, blast radius from the dependency graph when the caller
// has one. Zero blast radius is honest when there is no graph.
func RiskFromConfig(c *config.Config, deps ...rollout.Dependency) RiskInputs {
	in := RiskInputs{
		ChangeType:  "config",
		Environment: c.Spec.Target.Env,
	}
	if c.Spec.DatabaseMigrate() != nil {
		in.ChangeType = "schema"
	}
	if len(deps) > 0 {
		nodes := []string{c.Spec.Target.Ref}
		g := depgraph.New(nodes, deps)
		in.BlastRadius = g.BlastRadius(c.Spec.Target.Ref)
	}
	return in
}

// EvaluateRisk runs the blast-radius gate for a config + rollout-time inputs.
// Callers set ApplyRequest.NeedsApproval from the returned Decision.
func (e *Engine) EvaluateRisk(ctx context.Context, c *config.Config, in RiskInputs) (risk.Decision, error) {
	weights, recentFailures, err := e.riskWeights(ctx, c)
	if err != nil {
		return risk.Decision{}, err
	}
	g := risk.Gate{
		Threshold:     c.Spec.Risk.Threshold,
		Weights:       weights,
		SensitiveExpr: c.Spec.Risk.Sensitive,
	}
	return g.Evaluate(risk.Signals{
		Criticality:    c.Spec.Target.Criticality,
		Environment:    in.Environment,
		ChangeType:     in.ChangeType,
		BlastRadius:    in.BlastRadius,
		Strategy:       c.Spec.Strategy.Type,
		RecentFailures: recentFailures,
	})
}

func (e *Engine) riskWeights(ctx context.Context, c *config.Config) (risk.Weights, int, error) {
	w := risk.DefaultWeights()
	h := c.Spec.Risk.History
	if h.Lookback <= 0 && h.Weight == 0 && h.MaxFailures == 0 {
		return w, 0, nil
	}
	if h.Lookback <= 0 {
		h.Lookback = 10
	}
	if h.Weight == 0 {
		h.Weight = 0.15
	}
	if h.MaxFailures <= 0 {
		h.MaxFailures = 3
	}
	w.History = h.Weight
	w.MaxRecentFailures = h.MaxFailures

	hist, err := e.store.History(ctx, c.Spec.Target.Ref)
	if err != nil {
		return risk.Weights{}, 0, fmt.Errorf("engine: risk history: %w", err)
	}
	recentFailures := 0
	for i, rec := range hist {
		if i >= h.Lookback {
			break
		}
		if rec.Phase == rollout.PhaseRolledBack {
			recentFailures++
		}
	}
	return w, recentFailures, nil
}

// ApplyRequest drives Apply.
type ApplyRequest struct {
	Config    *config.Config
	Initiator rollout.Identity
	// Root is the config-file directory that relative referenced manifest
	// sources (manifestFrom) resolve against. In-process callers set it (the
	// reconcile watcher, the one-shot CLI); when empty Apply falls back to the
	// context (see WithRoot). Remote gRPC applies leave it empty.
	Root string
	// Planned records that a plan/diff was produced first. Agent-driven
	// rollouts must set this — an agent cannot apply blind.
	Planned bool
	// NeedsApproval forces the approval gate regardless of the computed risk
	// (callers may pre-decide; the engine also computes risk when configured).
	NeedsApproval bool
	// Risk inputs the engine cannot derive from config alone. Used by the risk
	// gate and the guardrails policy floor.
	Risk RiskInputs
}

// Apply deploys the desired state to the target and persists the rollout,
// driving phases through the statekit lifecycle so every transition is legal.
// On target failure the rollout is recorded as rolled-back and the error
// returned. Zero-pause plans drain in this call and advance to verifying;
// a positive canary pause persists a stepper snapshot and returns deploying
// so Tick (and the reconcile loop) can finish the bake. If the gate requires
// approval the rollout halts at awaiting-approval and the target is not touched.
func (e *Engine) Apply(ctx context.Context, req ApplyRequest) (*rollout.Rollout, error) {
	cfg := req.Config
	ref := cfg.Spec.Target.Ref
	if req.Initiator.Kind == "agent" && !req.Planned {
		return nil, fmt.Errorf("engine: apply: agent-driven rollout requires a produced plan first")
	}

	// 1. Guardrails: emergency freeze, agent rate-limit, non-bypassable policy
	//    floor. The floor can force approval regardless of the computed score.
	needApproval := req.NeedsApproval
	if e.guardrails != nil {
		force, err := e.guardrails.CheckApply(ctx, req.Initiator, security.FloorInput{
			TargetRef:   ref,
			Environment: req.Risk.Environment,
			ChangeType:  req.Risk.ChangeType,
			Criticality: cfg.Spec.Target.Criticality,
		})
		if err != nil {
			e.record(audit.Entry{Action: audit.ActionApply, TargetRef: ref, Actor: req.Initiator, Detail: "blocked: " + err.Error()})
			return nil, err
		}
		needApproval = needApproval || force
	}

	// 2. Risk gate (only when configured — threshold>0 or a sensitive expr).
	var riskScore float64
	if cfg.Spec.Risk.Threshold > 0 || cfg.Spec.Risk.Sensitive != "" {
		d, err := e.EvaluateRisk(ctx, cfg, req.Risk)
		if err != nil {
			return nil, err
		}
		riskScore = d.Score
		needApproval = needApproval || d.NeedsApproval
	}

	// 3. External governance, when a provider is configured. Unlike the gates above,
	//    this can refuse outright rather than escalate to approval: the point of
	//    delegating a decision is that the answer is binding. Escalating instead would
	//    let an approver here overrule a system that was asked precisely because it
	//    knows something this engine does not.
	//
	//    Checked before the target lock, so a refusal costs nothing and no manifest is
	//    built for a rollout that will not happen.
	if decision, err := e.governance.Evaluate(ctx, governance.Request{
		Action:      "apply",
		TargetRef:   ref,
		Actor:       req.Initiator,
		Environment: governanceEnvironment(cfg, req),
		Version:     cfg.Metadata.Labels[LabelVersion],
	}); err != nil || !decision.Allowed {
		// An unreachable governor and a refusing one both stop the deploy, but they are
		// different problems and the audit entry says which. Reporting an outage as a
		// policy refusal would send someone to read a policy that never ran.
		reason := decision.Reason
		if err != nil {
			reason = "governance is unavailable: " + err.Error()
		}
		e.record(audit.Entry{
			Action: audit.ActionApply, TargetRef: ref, Actor: req.Initiator,
			Detail: "blocked by governance: " + reason,
		})
		return nil, fmt.Errorf("engine: apply: blocked by governance: %s", reason)
	}

	release, ok, err := e.acquireTarget(ctx, ref)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrTargetBusy
	}
	defer release()

	if _, found, err := e.InFlight(ctx, ref); err != nil {
		return nil, err
	} else if found {
		return nil, ErrTargetBusy
	}

	root := req.Root
	if root == "" {
		root = rootFromContext(ctx)
	}
	m, err := manifestFromConfig(cfg, root)
	if err != nil {
		return nil, err
	}
	// Referenced sources (manifestFrom) stamp the drift checksum over the rendered
	// output — the annotation the target records and Observe reads back — so it
	// must be computed before the manifest is applied.
	if _, err := e.stampReferencedChecksum(ctx, ref, &m); err != nil {
		return nil, err
	}

	// 3. Resolve secret references in the target spec, then build the target.
	tcfg, err := e.resolveSecrets(ctx, cfg.Spec.Target)
	if err != nil {
		return nil, err
	}
	tgt, err := e.build(tcfg)
	if err != nil {
		return nil, err
	}
	defer closeTarget(tgt)

	// 4. Artifact provenance: independently verify what is about to ship.
	if e.artifact != nil {
		if image := tcfg.Spec["image"]; image != nil {
			if s, _ := image.(string); s != "" {
				if err := e.artifact.Check(ctx, s); err != nil {
					e.record(audit.Entry{Action: audit.ActionApply, TargetRef: ref, Actor: req.Initiator, Detail: "artifact rejected: " + err.Error()})
					return nil, err
				}
			}
		}
	}

	// 5. Lifecycle: validating → gate.
	lc, err := rollout.NewLifecycle(rollout.LifeContext{
		PlanProduced:  req.Planned || req.Initiator.Kind != "agent",
		NeedsApproval: needApproval,
	})
	if err != nil {
		return nil, err
	}
	if _, err := lc.Send(rollout.EventValidate); err != nil {
		return nil, err
	}
	if _, err := lc.Send(rollout.EventGate); err != nil {
		return nil, err
	}

	now := e.now()
	r := rollout.Rollout{
		ID:        e.newID(),
		TargetRef: ref,
		Phase:     lc.Phase(), // deploying, or awaiting-approval if gated
		Strategy:  strategyFrom(cfg),
		Desired:   m,
		RiskScore: riskScore,
		Initiator: req.Initiator,
		CreatedAt: now,
		UpdatedAt: now,
	}
	// Capture the database hooks so a later manual/agent rollback can run the
	// reverse command (not just the auto path that still holds the config) and so
	// a rollback can be gated on the migration's backward-compatibility.
	if db := cfg.Spec.DatabaseRollbackHook(); db != nil {
		r.DBRollbackCmd = db.Command
		r.DBRollbackTimeout = db.Timeout
	}
	if mig := cfg.Spec.DatabaseMigrate(); mig != nil {
		r.DBMigrateCmd = mig.Command
		r.DBMigrateTimeout = mig.Timeout
		r.DBMigrateWhen = cfg.Spec.DatabaseMigrateWhen()
	}
	r.DBBackwardCompatible = cfg.Spec.DatabaseBackwardCompatible()
	// Capture the progressive-delivery descriptors (traffic router + coupled
	// feature flag) so a later rollback — auto, manual, or agent-driven — can
	// reset delivery (traffic → stable, flag disabled) without the config in
	// hand. Mirrors the DB-hook capture above; opaque JSON keeps this model
	// decoupled from the config package.
	if tr := cfg.Spec.TrafficRouting; tr != nil {
		if b, mErr := json.Marshal(tr); mErr == nil {
			r.DeliveryTraffic = b
		}
	}
	if ff := cfg.Spec.FeatureFlags; ff != nil {
		if b, mErr := json.Marshal(ff); mErr == nil {
			r.DeliveryFlag = b
		}
	}
	// Capture the metric-analysis descriptor so a later manual Verify — and the
	// Promote that follows — can run the same analysis gate as the auto path,
	// which still holds the config. Empty means no analysis was configured.
	// Mirrors the delivery-descriptor capture above; opaque JSON keeps this model
	// decoupled from the config package.
	if an := cfg.Spec.Analysis; an != nil {
		if b, mErr := json.Marshal(an); mErr == nil {
			r.Analysis = b
		}
	}
	// Same for the smoke test, so manual Verify/Promote run the same gate as the
	// auto path. Empty means no smoke test was configured.
	if st := cfg.Spec.Rollback.SmokeTest; st != nil && len(st.Command) > 0 {
		if b, mErr := json.Marshal(st); mErr == nil {
			r.SmokeTest = b
		}
	}
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return nil, err
	}
	e.record(audit.Entry{Action: audit.ActionApply, RolloutID: r.ID, TargetRef: ref, Phase: string(r.Phase), Actor: req.Initiator})
	if r.Phase == rollout.PhaseAwaitingApproval {
		e.notifyEvent(ctx, notify.Event{Kind: notify.ApprovalNeeded, TargetRef: ref, RolloutID: r.ID})
		return &r, nil // halt: gate requires human approval, target untouched
	}

	// 6. Progressive deploy: apply the desired state once, then drive the
	//    canary Stepper. Zero-pause plans drain here and return verifying;
	//    a positive pause persists a snapshot and returns deploying so Tick
	//    (reconcile) can finish the bake without blocking this call.
	if err := e.deployOnce(ctx, cfg, tgt, m); err != nil {
		return e.failProgressive(ctx, lc, &r, cfg, m, err, req.Initiator)
	}
	return e.startStepper(ctx, lc, &r, cfg, tgt, req.Initiator)
}

// VerifyOutcome reports how the post-deploy gate resolved.
type VerifyOutcome struct {
	Rollout    rollout.Rollout
	RolledBack bool
	Reason     string // why it rolled back (empty on success)
}

// VerifyOrRollback runs the observability-free post-deploy gate: the target
// Health() check and an optional smoke test (run-this-expect-exit-0). If either
// fails and the config opts into auto-rollback, it re-applies the prior manifest
// and the rollout ends rolled-back; otherwise it promotes. A step error/timeout
// during the deploy itself is handled earlier in Apply — this covers the other
// two v1 auto-rollback signals.
func (e *Engine) VerifyOrRollback(ctx context.Context, rolloutID string, prior pt.Manifest, c *config.Config) (VerifyOutcome, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return VerifyOutcome{}, err
	}
	// A prior loaded from history has no Root (never persisted). Referenced
	// sources restore their captured Rendered bytes verbatim (no re-render, no
	// Root); the re-root here only helps inline/flat sources and legacy history
	// entries that predate captured Rendered.
	if prior.Root == "" {
		prior.Root = rootFromContext(ctx)
	}
	auto := c.Spec.Rollback.Auto

	failed, reason, successNote := e.runPostDeployChecks(ctx, r, c)
	if failed {
		if !auto {
			return VerifyOutcome{Rollout: r, Reason: reason}, fmt.Errorf("engine: verify failed (auto-rollback disabled): %s", reason)
		}
		// Auto-rollback forces past the backward-compatibility gate: the deploy
		// already failed, so recovering to the prior state beats leaving it up.
		rb, err := e.rollbackWithDatabase(ctx, rolloutID, prior, reason, c.Spec.DatabaseRollbackHook(), true)
		if err != nil {
			return VerifyOutcome{Rollout: r, Reason: reason}, fmt.Errorf("engine: auto-rollback after %q: %w", reason, err)
		}
		e.notifyDeployment(ctx, notify.RolledBack, rb, reason)
		return VerifyOutcome{Rollout: rb, RolledBack: true, Reason: reason}, nil
	}
	promoted, err := e.promoteWithNote(ctx, rolloutID, successNote)
	if err != nil {
		return VerifyOutcome{}, err
	}
	// Promotion completes the rollout: drive the coupled flag to 100%.
	if flagsEnabled(c.Spec.FeatureFlags, "promote") {
		e.driveFlag(ctx, promoted.TargetRef, c.Spec.FeatureFlags, 100)
	}
	e.notifyDeployment(ctx, notify.Promoted, promoted, "")
	return VerifyOutcome{Rollout: promoted}, nil
}

// Post-deploy gate names, stable identifiers in VerifyReport output.
const (
	GateHealth   = "health"
	GateSmoke    = "smoke"
	GateAnalysis = "analysis"
)

// Post-deploy gate outcomes.
const (
	GatePass    = "pass"
	GateFail    = "fail"
	GateSkipped = "skipped" // not configured (or analysis not enabled)
	GateNotRun  = "not-run" // short-circuited by an earlier failure
)

// GateResult is one post-deploy gate's outcome.
type GateResult struct {
	Gate   string `json:"gate"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// gateSet is the resolved set of post-deploy gates to run. It is built either
// from the live config (the auto path, which still holds it) or from the
// descriptors captured on the rollout at deploy time (the manual paths), so
// both drive the SAME gate implementations and cannot drift apart.
type gateSet struct {
	health   bool
	smoke    *config.SmokeTest
	analysis *config.Analysis
}

// gatesFromConfig resolves the gate set for the auto path. The health gate runs
// when a health check is configured or auto-rollback is on, matching the
// original post-deploy behaviour.
func (e *Engine) gatesFromConfig(c *config.Config) gateSet {
	gs := gateSet{
		health: c.Spec.Rollback.HealthCheck != nil || c.Spec.Rollback.Auto,
		smoke:  c.Spec.Rollback.SmokeTest,
	}
	// Metric-based analysis is a stable Phase 2 feature. It remains opt-in at
	// engine construction so v1 rollback stays observability-free by default.
	if e.analysis {
		gs.analysis = c.Spec.Analysis
	}
	return gs
}

// gatesFromRollout resolves the gate set from the descriptors captured on the
// rollout at deploy time, for the manual paths that do not hold the config.
// Health always runs — a manual verification with no health signal is not a
// verification. Fails CLOSED on an undecodable descriptor: a gate that cannot
// be read is not a pass.
func (e *Engine) gatesFromRollout(r rollout.Rollout) (gateSet, error) {
	gs := gateSet{health: true}
	if len(r.SmokeTest) > 0 {
		var st config.SmokeTest
		if err := json.Unmarshal(r.SmokeTest, &st); err != nil {
			return gateSet{}, fmt.Errorf("smoke test descriptor unreadable: %w", err)
		}
		gs.smoke = &st
	}
	if e.analysis && len(r.Analysis) > 0 {
		var a config.Analysis
		if err := json.Unmarshal(r.Analysis, &a); err != nil {
			return gateSet{}, fmt.Errorf("analysis descriptor unreadable: %w", err)
		}
		gs.analysis = &a
	}
	return gs, nil
}

// runGates runs the post-deploy gates in order (health → smoke → analysis) and
// reports every one. It short-circuits on the first failure — the same order and
// stopping behaviour as the deploy path, so a dry-run predicts what a real
// verification would do — marking the gates it never reached as not-run.
func (e *Engine) runGates(ctx context.Context, r rollout.Rollout, gs gateSet) []GateResult {
	out := make([]GateResult, 0, 3)
	failed := false
	run := func(enabled bool, skipDetail string, gate func() GateResult) {
		switch {
		case failed:
			out = append(out, GateResult{Gate: gateNameOf(len(out)), Status: GateNotRun})
		case !enabled:
			out = append(out, GateResult{Gate: gateNameOf(len(out)), Status: GateSkipped, Detail: skipDetail})
		default:
			res := gate()
			out = append(out, res)
			failed = res.Status == GateFail
		}
	}
	run(gs.health, "no health check configured", func() GateResult { return e.gateHealth(ctx, r) })
	run(gs.smoke != nil && len(gs.smoke.Command) > 0, "no smoke test configured", func() GateResult {
		return e.gateSmoke(ctx, gs.smoke)
	})
	run(gs.analysis != nil, "no metric analysis configured", func() GateResult {
		return e.gateAnalysis(ctx, gs.analysis)
	})
	return out
}

// gateNameOf maps a gate's position in the fixed run order to its name.
func gateNameOf(i int) string {
	switch i {
	case 0:
		return GateHealth
	case 1:
		return GateSmoke
	default:
		return GateAnalysis
	}
}

func (e *Engine) gateHealth(ctx context.Context, r rollout.Rollout) GateResult {
	tgt, err := e.buildTarget(r.TargetRef, r.Desired)
	if err != nil {
		// Fail CLOSED: a health gate we cannot even build is not a pass. Letting
		// a build error fall through would promote an unverified deploy, the
		// same failure mode the step gate already guards against.
		return GateResult{Gate: GateHealth, Status: GateFail, Detail: "health gate unavailable: " + err.Error()}
	}
	defer closeTarget(tgt)
	hs, herr := tgt.Health(ctx)
	if herr != nil || hs.State == pt.HealthUnhealthy {
		detail := "health check failed"
		if hs.Reason != "" {
			detail = "health check failed: " + hs.Reason
		}
		return GateResult{Gate: GateHealth, Status: GateFail, Detail: detail}
	}
	// A degraded target still passes (only Unhealthy fails), so report which.
	return GateResult{Gate: GateHealth, Status: GatePass, Detail: healthStateName(hs.State)}
}

// healthStateName renders a health verdict for report output. Kept local rather
// than a String() on pkg/target.HealthState, which would change how the state
// formats everywhere it is already logged.
func healthStateName(s pt.HealthState) string {
	switch s {
	case pt.HealthHealthy:
		return "healthy"
	case pt.HealthDegraded:
		return "degraded"
	case pt.HealthUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

func (e *Engine) gateSmoke(ctx context.Context, st *config.SmokeTest) GateResult {
	code, err := e.smoke.Run(ctx, st.Command)
	if err != nil {
		return GateResult{Gate: GateSmoke, Status: GateFail, Detail: "smoke test error: " + err.Error()}
	}
	if code != st.ExpectExit {
		return GateResult{Gate: GateSmoke, Status: GateFail, Detail: fmt.Sprintf("smoke test exit %d (expected %d)", code, st.ExpectExit)}
	}
	return GateResult{Gate: GateSmoke, Status: GatePass}
}

func (e *Engine) gateAnalysis(ctx context.Context, a *config.Analysis) GateResult {
	ok, note := e.runAnalysis(ctx, a)
	if !ok {
		return GateResult{Gate: GateAnalysis, Status: GateFail, Detail: note}
	}
	return GateResult{Gate: GateAnalysis, Status: GatePass, Detail: note}
}

// firstFailure returns the detail of the first failed gate, or "" if all passed.
func firstFailure(gates []GateResult) string {
	for _, g := range gates {
		if g.Status == GateFail {
			return g.Detail
		}
	}
	return ""
}

// runPostDeployChecks returns (failed, failure reason, success note) for the
// health, smoke, and optional metric-analysis gates. It is the auto path's view
// of the shared gate runner.
func (e *Engine) runPostDeployChecks(ctx context.Context, r rollout.Rollout, c *config.Config) (bool, string, string) {
	gates := e.runGates(ctx, r, e.gatesFromConfig(c))
	if reason := firstFailure(gates); reason != "" {
		return true, reason, ""
	}
	// A passing analysis may carry a note worth persisting on the promotion.
	for _, g := range gates {
		if g.Gate == GateAnalysis && g.Status == GatePass && g.Detail != "" {
			return false, "", g.Detail
		}
	}
	return false, "", ""
}

// runAnalysis builds an analyzer from config (using the injected metrics
// provider, or a Prometheus provider from the config address) and runs it.
func (e *Engine) runAnalysis(ctx context.Context, a *config.Analysis) (bool, string) {
	provider := e.metrics
	if provider == nil {
		switch {
		case a.Plugin != "":
			// A metricprovider plugin supplies the backend (Datadog, CloudWatch,
			// a custom metrics service). Launched per analysis run, then closed.
			p, err := e.metricsBuild(ctx, a)
			if err != nil {
				return false, "analysis: " + err.Error()
			}
			if c, ok := p.(interface{ Close() error }); ok {
				defer func() { _ = c.Close() }()
			}
			provider = p
		case a.Provider == "prometheus":
			provider = analysis.Prometheus{Addr: a.Address}
		default:
			return false, fmt.Sprintf("analysis: no metrics provider for %q", a.Provider)
		}
	}
	metrics := make([]analysis.Metric, 0, len(a.Metrics))
	for _, m := range a.Metrics {
		metrics = append(metrics, analysis.Metric{Name: m.Name, Query: m.Query, Aggregation: m.Aggregation})
	}
	interval, _ := time.ParseDuration(a.Interval)
	an, err := analysis.New(provider, analysis.Template{
		Metrics:      metrics,
		Condition:    a.Condition,
		Interval:     interval,
		Count:        a.Count,
		FailureLimit: a.FailureLimit,
	})
	if err != nil {
		return false, "analysis: " + err.Error()
	}
	res := an.Run(ctx)
	if !res.Passed {
		return false, "analysis failed: " + res.Reason
	}
	return true, fmt.Sprintf("analysis passed: %d measurement(s)", len(res.Measurements))
}

// Approve resolves an awaiting-approval rollout: it deploys to the target and
// advances to verifying. This is the human/agent "approve" arm of the single
// risk gate.
func (e *Engine) Approve(ctx context.Context, rolloutID string, by rollout.Identity) (rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return rollout.Rollout{}, err
	}
	// Separation of duties (four-eyes): the approver must differ from the
	// initiator, so one principal cannot both propose and approve a gated
	// rollout. Exempt system/reconcile initiators (empty or "system" name) — they
	// are not real approvers to collude with. Opt out for single-operator setups
	// via ROLLOPS_ALLOW_SELF_APPROVE=1; defaults to ENFORCE.
	if !allowSelfApprove() && realPrincipal(r.Initiator) && sameIdentity(by, r.Initiator) {
		e.record(audit.Entry{Action: audit.ActionApply, RolloutID: r.ID, TargetRef: r.TargetRef, Actor: by, Detail: "approve rejected: four-eyes (approver == initiator)"})
		return r, fmt.Errorf("engine: four-eyes: approver %q must differ from initiator %q (set ROLLOPS_ALLOW_SELF_APPROVE=1 to allow)", by.Name, r.Initiator.Name)
	}
	release, ok, err := e.acquireTarget(ctx, r.TargetRef)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, ErrTargetBusy
	}
	defer release()

	// Re-check the guardrails at approve time: an emergency freeze engaged after
	// the rollout was gated must block the approve-driven deploy too, otherwise
	// approval is a freeze bypass.
	if e.guardrails != nil {
		if _, gErr := e.guardrails.CheckApply(ctx, by, security.FloorInput{TargetRef: r.TargetRef}); gErr != nil {
			e.record(audit.Entry{Action: audit.ActionApply, RolloutID: r.ID, TargetRef: r.TargetRef, Actor: by, Detail: "approve blocked: " + gErr.Error()})
			return r, gErr
		}
	}

	lc, err := rollout.ResumeLifecycle(r.Phase, rollout.LifeContext{PlanProduced: true})
	if err != nil {
		return rollout.Rollout{}, err
	}
	if _, err := lc.Send(rollout.EventApprove); err != nil {
		return r, err // not awaiting approval
	}
	tgt, err := e.buildTarget(r.TargetRef, r.Desired)
	if err != nil {
		return r, err
	}
	defer closeTarget(tgt)
	if _, err := tgt.Apply(ctx, r.Desired); err != nil {
		_, _ = lc.Send(rollout.EventError)
		r.Phase = lc.Phase()
		r.UpdatedAt = e.now()
		_ = e.store.SaveRollout(ctx, r)
		return r, fmt.Errorf("engine: approve: apply: %w", err)
	}
	if _, err := lc.Send(rollout.EventDeployed); err != nil {
		return r, err
	}
	r.Phase = lc.Phase()
	if by.Name != "" {
		r.Initiator = by
	}
	r.UpdatedAt = e.now()
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return rollout.Rollout{}, err
	}
	return r, nil
}

// allowSelfApprove reports whether the four-eyes separation-of-duties check is
// disabled via ROLLOPS_ALLOW_SELF_APPROVE=1 (single-operator setups). Read once
// at first use so the policy is stable for the process lifetime.
var allowSelfApprove = sync.OnceValue(func() bool {
	return os.Getenv("ROLLOPS_ALLOW_SELF_APPROVE") == "1"
})

// realPrincipal reports whether an identity is a real, colludable approver.
// System/reconcile initiators (empty kind/name, or the "system" name) are exempt
// from four-eyes: they are not a human who could self-approve.
func realPrincipal(id rollout.Identity) bool {
	return id.Kind != "" && id.Name != "" && id.Name != "system"
}

// sameIdentity reports whether two identities are the same principal.
func sameIdentity(a, b rollout.Identity) bool {
	return a.Kind == b.Kind && a.Name == b.Name
}

// Reject resolves an awaiting-approval rollout by rolling it back without
// touching the target.
func (e *Engine) Reject(ctx context.Context, rolloutID string, by rollout.Identity) (rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return rollout.Rollout{}, err
	}
	lc, err := rollout.ResumeLifecycle(r.Phase, rollout.LifeContext{PlanProduced: true})
	if err != nil {
		return rollout.Rollout{}, err
	}
	if _, err := lc.Send(rollout.EventReject); err != nil {
		return r, err
	}
	r.Phase = lc.Phase()
	if by.Name != "" {
		r.Initiator = by
	}
	r.UpdatedAt = e.now()
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return rollout.Rollout{}, err
	}
	return r, nil
}

// Observe queries the target's live fingerprint and records it for drift
// detection. Returns the fingerprint observed.
func (e *Engine) Observe(ctx context.Context, t config.Target) (pt.Fingerprint, error) {
	tgt, err := e.build(t)
	if err != nil {
		return pt.Fingerprint{}, err
	}
	defer closeTarget(tgt)
	fp, err := tgt.Observe(ctx)
	if err != nil {
		return pt.Fingerprint{}, fmt.Errorf("engine: observe: %w", err)
	}
	if err := e.store.SaveObservedState(ctx, rollout.TargetState{
		TargetRef:  t.Ref,
		Observed:   fp,
		ObservedAt: e.now(),
	}); err != nil {
		return pt.Fingerprint{}, err
	}
	return fp, nil
}

// VerifyReport is the outcome of a dry-run verification: every post-deploy gate
// with its status, and whether the rollout would pass. The rollout's phase is
// never changed by producing it.
type VerifyReport struct {
	RolloutID string          `json:"rolloutId"`
	TargetRef string          `json:"targetRef"`
	Phase     string          `json:"phase"`
	OK        bool            `json:"ok"`
	Reason    string          `json:"reason,omitempty"` // first failing gate's detail
	Gates     []GateResult    `json:"gates"`
	Rollout   rollout.Rollout `json:"-"`
}

// Verify is a DRY RUN of the post-deploy gate: it runs the same health, smoke,
// and metric-analysis checks as the auto path (VerifyOrRollback) against the
// descriptors captured on the rollout at deploy time, reports each one, and
// changes nothing — no phase transition, no promotion, no rollback. Use it to
// answer "would this promote?" before committing to Promote.
//
// A failing gate is a RESULT, not an error: the report comes back with OK=false
// and the reason. The error return is reserved for operational failures (the
// rollout cannot be loaded, a captured descriptor is unreadable) — those fail
// CLOSED, since a gate that cannot be read is not a pass.
//
// Note the gates run for real: a configured smoke test executes its command on
// the daemon host (under the same confinement policy as the auto path), and
// analysis queries the metrics backend. "Dry run" means nothing is *changed*,
// not that nothing is *run*.
func (e *Engine) Verify(ctx context.Context, rolloutID string) (VerifyReport, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return VerifyReport{}, err
	}
	gs, err := e.gatesFromRollout(r)
	if err != nil {
		return VerifyReport{}, fmt.Errorf("engine: verify: %w", err)
	}
	gates := e.runGates(ctx, r, gs)
	reason := firstFailure(gates)
	return VerifyReport{
		RolloutID: r.ID,
		TargetRef: r.TargetRef,
		Phase:     string(r.Phase),
		OK:        reason == "",
		Reason:    reason,
		Gates:     gates,
		Rollout:   r,
	}, nil
}

// Promote marks a verified rollout as promoted, gated on the SAME post-deploy
// checks Verify dry-runs: health, smoke, and metric analysis, in that order.
// Because a freshly-deployed rollout sits in `verifying` and the lifecycle does
// not force a prior Verify, an ungated Promote would be a way to skip the gate
// entirely. The gates live here (not in promoteWithNote) so the auto path
// (VerifyOrRollback), which has already run them before calling promoteWithNote,
// does not run them twice.
//
// force overrides the gates — the break-glass path for when the gates are
// themselves wrong (a flaky probe, a metrics backend that is down). It mirrors
// rollback's force flag, and is recorded in the audit trail and on the rollout's
// note so the bypass is never silent.
//
// Verify is the dry run of exactly this: `verify` passing means `promote`
// passes.
func (e *Engine) Promote(ctx context.Context, rolloutID string, by rollout.Identity, force bool) (rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return rollout.Rollout{}, err
	}
	note := ""
	if force {
		note = "promote: post-deploy gates bypassed (forced)"
	} else {
		gs, gErr := e.gatesFromRollout(r)
		if gErr != nil {
			return r, fmt.Errorf("engine: promote: %w", gErr)
		}
		if reason := firstFailure(e.runGates(ctx, r, gs)); reason != "" {
			return r, fmt.Errorf("engine: promote: %s; force the promote to override", reason)
		}
	}
	promoted, err := e.promoteWithNote(ctx, rolloutID, note)
	if err != nil {
		return promoted, err
	}
	detail := "promoted"
	if force {
		detail = "promoted (post-deploy gates bypassed: forced)"
	}
	e.record(audit.Entry{
		Action: audit.ActionPromote, RolloutID: promoted.ID, TargetRef: promoted.TargetRef,
		Phase: string(promoted.Phase), Actor: by, Detail: detail,
	})
	return promoted, nil
}

// Freeze engages or lifts the emergency kill-switch: while engaged, every apply
// is blocked by the guardrails. The new state is written to the Store before the
// in-memory switch flips, so a crash mid-toggle cannot silently lift a freeze
// (persist-then-engage) and a failed persist leaves the previous in-memory
// state. Returns the resulting state.
func (e *Engine) Freeze(ctx context.Context, on bool, by rollout.Identity, reason string) (active bool, activeReason string, err error) {
	if e.guardrails == nil || e.guardrails.Freeze == nil {
		return false, "", fmt.Errorf("engine: freeze is not configured")
	}
	state := store.FreezeState{Active: on, Reason: reason, By: by, At: e.now()}
	if !on {
		state.Reason = ""
	}
	if err := e.store.SaveFreeze(ctx, state); err != nil {
		return false, "", fmt.Errorf("engine: persist freeze: %w", err)
	}
	if on {
		e.guardrails.Freeze.Engage(by, reason)
	} else {
		e.guardrails.Freeze.Lift(by)
	}
	active, activeReason = e.guardrails.Freeze.Active()
	detail := "freeze lifted"
	if active {
		detail = "freeze engaged: " + activeReason
	}
	e.record(audit.Entry{Action: audit.ActionFreeze, Actor: by, Detail: detail})
	return active, activeReason, nil
}

// FreezeStatus reports whether the emergency freeze is engaged and its reason.
func (e *Engine) FreezeStatus() (active bool, reason string) {
	if e.guardrails == nil || e.guardrails.Freeze == nil {
		return false, ""
	}
	return e.guardrails.Freeze.Active()
}

func (e *Engine) promoteWithNote(ctx context.Context, rolloutID, note string) (rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return rollout.Rollout{}, err
	}
	lc, err := rollout.ResumeLifecycle(r.Phase, rollout.LifeContext{PlanProduced: true})
	if err != nil {
		return rollout.Rollout{}, err
	}
	if _, err := lc.Send(rollout.EventVerifyOK); err != nil {
		return r, err // not in a promotable phase
	}
	r.Phase = lc.Phase()
	// Post-promote forward migration (contract / data backfill) runs once the
	// rollout is promoted. A failure leaves the rollout promoted but records the
	// failure loudly, so the schema state gets operator attention.
	if r.DBMigrateWhen == config.MigratePostPromote && len(r.DBMigrateCmd) > 0 {
		mig := &config.DatabaseRollback{Command: r.DBMigrateCmd, Timeout: r.DBMigrateTimeout}
		if err := e.runDatabaseCommand(ctx, mig); err != nil {
			r.Note = appendNote(note, "database migrate (post-promote): failed: "+err.Error())
			r.UpdatedAt = e.now()
			_ = e.store.SaveRollout(ctx, r)
			return r, fmt.Errorf("database migrate (post-promote): %w", err)
		}
		note = appendNote(note, "database migrate (post-promote): succeeded")
	}
	r.Note = note
	r.UpdatedAt = e.now()
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return rollout.Rollout{}, err
	}
	return r, nil
}

// Rollback re-applies a prior manifest to the target — the observability-free
// recovery path, driveable manually, by an agent, or automatically. When force
// is false, a rollback is blocked if the release ran a database migration that
// was not declared backward-compatible and carries no reverse command (see the
// gate in rollbackWithDatabase); force overrides that guard.
func (e *Engine) Rollback(ctx context.Context, rolloutID string, prior pt.Manifest, force bool) (rollout.Rollout, error) {
	return e.rollbackWithNote(ctx, rolloutID, prior, "", force)
}

func (e *Engine) rollbackWithNote(ctx context.Context, rolloutID string, prior pt.Manifest, note string, force bool) (rollout.Rollout, error) {
	return e.rollbackWithDatabase(ctx, rolloutID, prior, note, nil, force)
}

func (e *Engine) rollbackWithDatabase(ctx context.Context, rolloutID string, prior pt.Manifest, note string, db *config.DatabaseRollback, force bool) (rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return rollout.Rollout{}, err
	}
	release, ok, err := e.acquireTarget(ctx, r.TargetRef)
	if err != nil {
		return r, err
	}
	if !ok {
		return r, ErrTargetBusy
	}
	defer release()
	return e.applyRollback(ctx, &r, prior, note, db, force)
}

// applyRollback is the lock-free rollback core: the backward-compatibility gate,
// the ROLLBACK lifecycle transition, re-applying the prior manifest, resetting
// progressive delivery (traffic → stable, coupled flag disabled), the optional
// reverse database command, and persistence.
//
// The caller MUST already hold the target lock: Apply's in-deploy failure path
// holds it via `defer release()`, and rollbackWithDatabase acquires it first.
// This helper never acquires the lock itself, so it can run from a path that
// already holds it without re-entering acquireTarget and deadlocking on
// ErrTargetBusy.
func (e *Engine) applyRollback(ctx context.Context, r *rollout.Rollout, prior pt.Manifest, note string, db *config.DatabaseRollback, force bool) (rollout.Rollout, error) {
	// When no explicit hook was passed (manual or agent rollback), fall back to
	// the command captured on the rollout at deploy time, so every rollback path —
	// not only auto — reverses the database.
	if db == nil && len(r.DBRollbackCmd) > 0 {
		db = &config.DatabaseRollback{Command: r.DBRollbackCmd, Timeout: r.DBRollbackTimeout}
	}

	// Backward-compatibility gate: if this release ran a forward migration that
	// was not declared backwardCompatible and there is no reverse command to undo
	// it, rolling the app back would run the old version against the new schema —
	// unsafe. Block it unless the caller forces. Auto-rollback passes force=true:
	// the deploy already failed, so leaving the bad version up is worse.
	if !force && len(r.DBMigrateCmd) > 0 && !r.DBBackwardCompatible && (db == nil || len(db.Command) == 0) {
		return *r, fmt.Errorf("engine: rollback blocked: release ran a database migration not declared backwardCompatible and has no database rollback command; force the rollback to override")
	}

	lc, err := rollout.ResumeLifecycle(r.Phase, rollout.LifeContext{PlanProduced: true})
	if err != nil {
		return rollout.Rollout{}, err
	}
	if _, err := lc.Send(rollout.EventRollback); err != nil {
		return *r, err // not in a rollbackable phase
	}
	tgt, err := e.buildTarget(r.TargetRef, prior)
	if err != nil {
		return *r, err
	}
	defer closeTarget(tgt)
	if _, err := tgt.Apply(ctx, prior); err != nil {
		return *r, fmt.Errorf("engine: rollback: re-apply: %w", err)
	}
	r.Phase = lc.Phase()
	r.Desired = prior
	r.UpdatedAt = e.now()
	// Reset the progressive-delivery plane so a rollback restores stable traffic
	// and turns the coupled flag off — not just the manifest. Best-effort, driven
	// from the descriptors persisted at deploy time (auto, manual, and agent
	// rollback all funnel through here).
	e.resetDelivery(ctx, r)
	if db != nil && len(db.Command) > 0 {
		if err := e.runDatabaseCommand(ctx, db); err != nil {
			r.Note = appendNote(note, "database rollback: failed: "+err.Error())
			_ = e.store.SaveRollout(ctx, *r)
			return *r, fmt.Errorf("database rollback: %w", err)
		}
		r.Note = appendNote(note, "database rollback: succeeded")
	} else {
		r.Note = note
	}
	if err := e.store.SaveRollout(ctx, *r); err != nil {
		return rollout.Rollout{}, err
	}
	return *r, nil
}

// resetDelivery reverts the progressive-delivery side effects of a rollout after
// a rollback: it shifts all traffic back to the stable backend (canary 0%) and
// disables the coupled feature flag. Best-effort, mirroring the forward drive —
// a delivery hiccup is audited but never blocks recovery. The descriptors come
// from the JSON captured on the rollout at deploy time, so manual and agent
// rollbacks (which hold no config) restore delivery just like the auto path.
func (e *Engine) resetDelivery(ctx context.Context, r *rollout.Rollout) {
	if len(r.DeliveryTraffic) > 0 {
		var tr config.TrafficRouting
		if err := json.Unmarshal(r.DeliveryTraffic, &tr); err == nil {
			_ = e.driveTraffic(ctx, r.TargetRef, &tr, 0)
		}
	}
	if len(r.DeliveryFlag) > 0 {
		var ff config.FeatureFlags
		if err := json.Unmarshal(r.DeliveryFlag, &ff); err == nil {
			e.driveFlagDisable(ctx, r.TargetRef, &ff)
		}
	}
}

// driveFlagDisable turns the coupled flag off (Disabled=true) through a freshly
// launched flag provider, then closes it — the rollback counterpart to
// driveFlag. Best-effort: a flag failure is audited and never aborts recovery.
// Disabling is unconditional (independent of the flag's `when`): a flag already
// at 0% is safe to disable, and a partially rolled-out flag must be turned off.
func (e *Engine) driveFlagDisable(ctx context.Context, ref string, ff *config.FeatureFlags) {
	prov, err := e.flagBuild(ctx, ff)
	if err != nil {
		e.record(audit.Entry{Action: audit.ActionRollback, TargetRef: ref, Detail: "featureflag build failed: " + err.Error()})
		return
	}
	if c, ok := prov.(interface{ Close() error }); ok {
		defer func() { _ = c.Close() }()
	}
	hook := featureflags.Hook{Provider: prov}
	if err := hook.Apply(ctx, featureflags.Change{Flag: ff.Flag, Environment: ff.Environment, Disabled: true}); err != nil {
		e.record(audit.Entry{Action: audit.ActionRollback, TargetRef: ref, Detail: "featureflag disable failed: " + err.Error()})
		return
	}
	e.record(audit.Entry{Action: audit.ActionRollback, TargetRef: ref, Detail: fmt.Sprintf("featureflag %q disabled (rollback)", ff.Flag)})
}

// runDatabaseCommand runs a database hook (forward migrate or reverse rollback)
// via the configured runner, honouring an optional per-command timeout.
func (e *Engine) runDatabaseCommand(ctx context.Context, db *config.DatabaseRollback) error {
	if db.Timeout != "" {
		d, err := time.ParseDuration(db.Timeout)
		if err != nil {
			return err
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d)
		defer cancel()
	}
	return e.dbRollback.Run(ctx, db.Command)
}

func appendNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}

// Status returns a rollout by id.
func (e *Engine) Status(ctx context.Context, id string) (rollout.Rollout, error) {
	return e.store.LoadRollout(ctx, id)
}

// ErrUnsupported is returned when a target lacks an optional capability
// (Differ/Inspector).
var ErrUnsupported = errors.New("engine: target does not support this operation")

// rawTarget builds an unwrapped target (capabilities like Differ/Inspector live
// on the concrete target, not the fortify wrapper). Read-only, so no resilience
// envelope is needed.
func (e *Engine) rawTarget(ref string, m pt.Manifest) (pt.Target, error) {
	var spec map[string]any
	if len(m.Spec) > 0 {
		if err := json.Unmarshal(m.Spec, &spec); err != nil {
			return nil, fmt.Errorf("engine: decode manifest spec: %w", err)
		}
	}
	return e.reg.Build(config.Target{Kind: m.Kind, Ref: ref, Spec: spec})
}

// Diff returns the difference between a rollout's desired state and live state,
// when the target supports it (e.g. kubectl diff).
func (e *Engine) Diff(ctx context.Context, rolloutID string) (string, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return "", err
	}
	tgt, err := e.rawTarget(r.TargetRef, r.Desired)
	if err != nil {
		return "", err
	}
	d, ok := tgt.(pt.Differ)
	if !ok {
		return "", ErrUnsupported
	}
	return d.Diff(ctx, r.Desired)
}

// Resources lists the live resources a rollout's target manages, when the
// target supports it (e.g. kubectl get).
func (e *Engine) Resources(ctx context.Context, rolloutID string) ([]pt.Resource, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return nil, err
	}
	tgt, err := e.rawTarget(r.TargetRef, r.Desired)
	if err != nil {
		return nil, err
	}
	insp, ok := tgt.(pt.Inspector)
	if !ok {
		return nil, ErrUnsupported
	}
	return insp.Resources(ctx)
}

// priorManifest returns the most recent rollout's manifest for targetRef whose
// desired checksum differs from excludeChecksum — the "last known good" state to
// roll back to. Shared by RollbackLast (manual/UI) and Apply's auto-rollback so
// the prior-good selection is defined and tested in exactly one place.
func (e *Engine) priorManifest(ctx context.Context, targetRef, excludeChecksum string) (pt.Manifest, bool) {
	rs, err := e.store.ListRollouts(ctx, 0) // newest first
	if err != nil {
		return pt.Manifest{}, false
	}
	for i := range rs {
		if rs[i].TargetRef != targetRef {
			continue
		}
		if rs[i].Desired.Checksum != "" && rs[i].Desired.Checksum != excludeChecksum {
			return rs[i].Desired, true
		}
	}
	return pt.Manifest{}, false
}

// PriorManifest exposes the last-known-good manifest for a target (the most
// recent rollout whose desired checksum differs from excludeChecksum). The
// reconcile loop uses it to hand VerifyOrRollback the correct prior to revert
// to on a failed post-deploy gate, rather than the just-applied (broken)
// manifest. Returns false when the target has no distinct prior state.
func (e *Engine) PriorManifest(ctx context.Context, targetRef, excludeChecksum string) (pt.Manifest, bool) {
	return e.priorManifest(ctx, targetRef, excludeChecksum)
}

// RollbackLast rolls a target back to its previous distinct desired state by
// re-applying the prior rollout's manifest — the UI "Rollback" action. force
// overrides the backward-compatibility gate (see Rollback).
func (e *Engine) RollbackLast(ctx context.Context, targetRef string, force bool) (rollout.Rollout, error) {
	rs, err := e.store.ListRollouts(ctx, 0) // newest first
	if err != nil {
		return rollout.Rollout{}, err
	}
	var current *rollout.Rollout
	for i := range rs {
		if rs[i].TargetRef == targetRef {
			current = &rs[i]
			break
		}
	}
	if current == nil {
		return rollout.Rollout{}, fmt.Errorf("engine: no rollouts for target %q", targetRef)
	}
	prior, ok := e.priorManifest(ctx, targetRef, current.Desired.Checksum)
	if !ok {
		return rollout.Rollout{}, fmt.Errorf("engine: no prior state to roll back %q to", targetRef)
	}
	return e.Rollback(ctx, current.ID, prior, force)
}

// List returns the most recent rollouts, newest first.
func (e *Engine) List(ctx context.Context, limit int) ([]rollout.Rollout, error) {
	return e.store.ListRollouts(ctx, limit)
}

// History returns the audit/history records for a target, newest first.
func (e *Engine) History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error) {
	return e.store.History(ctx, targetRef)
}

// Schedule queues a rollout for a future time. A blank id is assigned.
func (e *Engine) Schedule(ctx context.Context, s rollout.ScheduledRollout) error {
	if s.ID == "" {
		s.ID = "sch-" + e.newID()
	}
	return e.store.Schedule(ctx, s)
}

// FireDueSchedules deploys every schedule due at now and removes it from the
// queue. Called on each reconcile tick. Per-schedule failures are collected and
// do not stop the others; the returned rollouts are those that were fired.
func (e *Engine) FireDueSchedules(ctx context.Context, now time.Time) ([]rollout.Rollout, error) {
	due, err := e.store.DueSchedules(ctx, now)
	if err != nil {
		return nil, err
	}
	var fired []rollout.Rollout
	var errs []error
	for _, s := range due {
		r, err := e.applyScheduled(ctx, s)
		if err != nil {
			errs = append(errs, fmt.Errorf("schedule %s: %w", s.ID, err))
			continue
		}
		if err := e.store.DeleteSchedule(ctx, s.ID); err != nil {
			errs = append(errs, err)
		}
		fired = append(fired, r)
	}
	return fired, errors.Join(errs...)
}

// applyScheduled deploys a pre-decided scheduled manifest (the gate decision was
// made when it was queued).
func (e *Engine) applyScheduled(ctx context.Context, s rollout.ScheduledRollout) (rollout.Rollout, error) {
	release, ok, err := e.acquireTarget(ctx, s.TargetRef)
	if err != nil {
		return rollout.Rollout{}, err
	}
	if !ok {
		return rollout.Rollout{}, ErrTargetBusy
	}
	defer release()

	// A scheduled apply must still clear the guardrails at FIRE time, not only at
	// queue time: an emergency freeze engaged after queuing must block it, and a
	// policy floor that demands human approval cannot be self-approved by a
	// scheduler. Without this the queue is a freeze/gate bypass.
	if e.guardrails != nil {
		force, gErr := e.guardrails.CheckApply(ctx, s.Initiator, security.FloorInput{TargetRef: s.TargetRef})
		if gErr != nil {
			e.record(audit.Entry{Action: audit.ActionApply, TargetRef: s.TargetRef, Actor: s.Initiator, Detail: "scheduled apply blocked: " + gErr.Error()})
			return rollout.Rollout{}, gErr
		}
		if force {
			e.record(audit.Entry{Action: audit.ActionApply, TargetRef: s.TargetRef, Actor: s.Initiator, Detail: "scheduled apply blocked: policy floor requires approval"})
			return rollout.Rollout{}, fmt.Errorf("engine: scheduled apply for %q requires approval and cannot self-approve", s.TargetRef)
		}
	}

	tgt, err := e.buildTarget(s.TargetRef, s.Desired)
	if err != nil {
		return rollout.Rollout{}, err
	}
	defer closeTarget(tgt)
	now := e.now()
	r := rollout.Rollout{
		ID:        e.newID(),
		TargetRef: s.TargetRef,
		Phase:     rollout.PhaseDeploying,
		Strategy:  rollout.StrategyRolling,
		Desired:   s.Desired,
		Initiator: s.Initiator,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return rollout.Rollout{}, err
	}
	if _, err := tgt.Apply(ctx, s.Desired); err != nil {
		r.Phase = rollout.PhaseRolledBack
		r.UpdatedAt = e.now()
		_ = e.store.SaveRollout(ctx, r)
		return r, fmt.Errorf("apply: %w", err)
	}
	r.Phase = rollout.PhaseVerifying
	r.UpdatedAt = e.now()
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return rollout.Rollout{}, err
	}
	return r, nil
}

// buildTarget reconstructs a bound Target from a persisted manifest.
func (e *Engine) buildTarget(ref string, m pt.Manifest) (pt.Target, error) {
	var spec map[string]any
	if len(m.Spec) > 0 {
		if err := json.Unmarshal(m.Spec, &spec); err != nil {
			return nil, fmt.Errorf("engine: decode manifest spec: %w", err)
		}
	}
	return e.build(config.Target{Kind: m.Kind, Ref: ref, Spec: spec})
}

// record emits an audit entry when an audit logger is configured.
func (e *Engine) record(en audit.Entry) {
	if e.audit != nil {
		e.audit.Record(en)
	}
}

// resolveSecrets replaces "secret:<ref>" string values in the target spec with
// the value resolved from the SecretProvider at execution time. Without a
// provider, the spec passes through unchanged. The resolved values are
// registered with the audit redactor so they never reach the trail.
func (e *Engine) resolveSecrets(ctx context.Context, t config.Target) (config.Target, error) {
	if e.secrets == nil || len(t.Spec) == 0 {
		return t, nil
	}
	const prefix = "secret:"
	out := make(map[string]any, len(t.Spec))
	for k, v := range t.Spec {
		s, ok := v.(string)
		if ok && strings.HasPrefix(s, prefix) {
			sec, err := e.secrets.Resolve(ctx, strings.TrimPrefix(s, prefix))
			if err != nil {
				return config.Target{}, fmt.Errorf("engine: resolve secret %q: %w", k, err)
			}
			if e.audit != nil {
				e.audit.Redactor().Register(sec)
			}
			out[k] = sec.Reveal()
			continue
		}
		out[k] = v
	}
	return config.Target{Kind: t.Kind, Ref: t.Ref, Criticality: t.Criticality, Spec: out}, nil
}

// manifestFromConfig builds the desired manifest from config. root is the
// config-file directory relative referenced sources resolve against — ambient
// execution context threaded through Plan/Apply, deliberately NOT folded into
// the checksum (the checksum stays a pure function of the desired spec, so the
// same config yields the same identity on every host and checkout dir).
func manifestFromConfig(c *config.Config, root string) (pt.Manifest, error) {
	spec, err := json.Marshal(c.Spec.Target.Spec)
	if err != nil {
		return pt.Manifest{}, fmt.Errorf("engine: marshal target spec: %w", err)
	}
	sum := sha256.Sum256(spec)

	// The declared environment is stamped onto the manifest so every notification
	// can say where a version went, without threading config through to each
	// notification site. An operator label of the same name wins: an explicit
	// statement outranks a derived one.
	labels := c.Metadata.Labels
	if c.Spec.Target.Env != "" {
		merged := make(map[string]string, len(labels)+1)
		for k, v := range labels {
			merged[k] = v
		}
		if _, set := merged[LabelEnvironment]; !set {
			merged[LabelEnvironment] = c.Spec.Target.Env
		}
		labels = merged
	}

	return pt.Manifest{
		Kind:     c.Spec.Target.Kind,
		Spec:     spec,
		Labels:   labels,
		Checksum: hex.EncodeToString(sum[:]),
		Root:     root,
	}, nil
}

// rootContextKey carries the config-file root (see manifestFromConfig) through
// Plan, whose signature is fixed by the Operations gRPC boundary. Apply takes it
// explicitly via ApplyRequest.Root, falling back to the context.
type rootContextKey struct{}

// WithRoot returns a context carrying the config-file root that relative
// referenced manifest sources resolve against. In-process callers (the reconcile
// watcher, the one-shot CLI) set it; remote gRPC callers leave it empty (the
// referenced files live on the client, not the daemon).
func WithRoot(ctx context.Context, root string) context.Context {
	if root == "" {
		return ctx
	}
	return context.WithValue(ctx, rootContextKey{}, root)
}

func rootFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(rootContextKey{}).(string); ok {
		return v
	}
	return ""
}

// stampReferencedChecksum re-keys a manifest's drift checksum over its RENDERED
// output when the target resolves it from a referenced external source
// (manifestFrom). This makes an edit to a referenced Kustomize/Helm/path file
// surface as drift even under shallow verification, where the spec bytes — and
// thus the spec-derived checksum — never change. Inline manifests and the legacy
// flat keys keep their spec checksum. It returns the rendered bytes (for the
// plan preview) or nil when the target does not render a referenced source.
func (e *Engine) stampReferencedChecksum(ctx context.Context, ref string, m *pt.Manifest) ([]byte, error) {
	raw, err := e.rawTarget(ref, *m)
	if err != nil {
		return nil, nil // best-effort: a target that will not build fails louder later
	}
	defer closeTarget(raw)
	r, ok := raw.(pt.Renderer)
	if !ok || !r.Referenced(*m) {
		return nil, nil
	}
	out, err := r.Render(ctx, *m)
	if err != nil {
		return nil, fmt.Errorf("engine: render referenced manifest: %w", err)
	}
	sum := sha256.Sum256(out)
	m.Checksum = hex.EncodeToString(sum[:])
	// Capture the rendered bytes so they are persisted with the rollout: a later
	// rollback restores exactly what was deployed rather than re-rendering the
	// referenced source (which may have changed, or be unreachable where no
	// checkout is at hand — the manual CLI/UI/API rollback path).
	m.Rendered = out
	return out, nil
}

func strategyFrom(c *config.Config) rollout.Strategy {
	switch c.Spec.Strategy.Type {
	case "canary":
		return rollout.StrategyCanary
	case "blue-green":
		return rollout.StrategyBlueGreen
	default:
		return rollout.StrategyRolling
	}
}

// BuildTarget constructs a target from its config through the registry.
//
// Exported for the orphan reaper (#154), which must act on a target whose
// RolloutConfig has been DELETED — so it cannot go through the normal plan
// path, which starts from a config that still exists. The registry is otherwise
// private to the engine and stays that way; this hands back a built target and
// nothing else.
func (e *Engine) BuildTarget(t config.Target) (pt.Target, error) {
	return e.reg.Build(t)
}
