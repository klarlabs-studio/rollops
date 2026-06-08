// Package engine is the central Go library of Rolloffs. Every interface — the
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
	"time"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/depgraph"
	"go.klarlabs.de/rolloffs/internal/risk"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/step"
	"go.klarlabs.de/rolloffs/internal/store"
	itarget "go.klarlabs.de/rolloffs/internal/target"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// Engine orchestrates rollouts over a Store and a target Registry.
type Engine struct {
	store  store.Store
	reg    *itarget.Registry
	locks  *keyedLocks
	policy step.Policy
	smoke  SmokeRunner
	now    func() time.Time
	newID  func() string
}

// Option configures an Engine.
type Option func(*Engine)

// WithClock overrides the time source (deterministic tests).
func WithClock(f func() time.Time) Option { return func(e *Engine) { e.now = f } }

// WithIDGen overrides the rollout id generator (deterministic tests).
func WithIDGen(f func() string) Option { return func(e *Engine) { e.newID = f } }

// WithPolicy overrides the fortify resilience policy applied to target ops.
func WithPolicy(p step.Policy) Option { return func(e *Engine) { e.policy = p } }

// WithSmokeRunner overrides the post-deploy smoke-test runner (tests).
func WithSmokeRunner(s SmokeRunner) Option { return func(e *Engine) { e.smoke = s } }

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
		store:  st,
		reg:    reg,
		locks:  newKeyedLocks(),
		policy: step.DefaultPolicy(),
		smoke:  execSmoke{},
		now:    func() time.Time { return time.Now().UTC() },
	}
	e.newID = func() string { return "ro-" + e.now().Format("20060102T150405.000000000") }
	for _, o := range opts {
		o(e)
	}
	return e
}

// Plan computes what an apply would change without applying it: the desired
// manifest, the target's current fingerprint, and whether they differ. Drift =
// current fingerprint != desired checksum.
func (e *Engine) Plan(ctx context.Context, c *config.Config) (*Plan, error) {
	m, err := manifestFromConfig(c)
	if err != nil {
		return nil, err
	}
	tgt, err := e.build(c.Spec.Target)
	if err != nil {
		return nil, err
	}
	cur, err := tgt.Observe(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: plan: observe: %w", err)
	}
	return newPlan(c.Spec.Target.Ref, m, cur), nil
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
	TargetRef string
	Desired   pt.Manifest
	Current   pt.Fingerprint
	Changed   bool
	Action    PlanAction
	Summary   string
}

func newPlan(ref string, desired pt.Manifest, current pt.Fingerprint) *Plan {
	changed := current.Value != desired.Checksum
	action := PlanNoop
	switch {
	case current.Value == "":
		action = PlanCreate
	case changed:
		action = PlanUpdate
	}
	p := &Plan{TargetRef: ref, Desired: desired, Current: current, Changed: changed, Action: action}
	p.Summary = p.render()
	return p
}

func (p *Plan) render() string {
	switch p.Action {
	case PlanNoop:
		return fmt.Sprintf("%s [%s]: no changes (checksum %s)", p.TargetRef, p.Desired.Kind, short(p.Desired.Checksum))
	case PlanCreate:
		return fmt.Sprintf("%s [%s]: create — deploy checksum %s (no current state observed)", p.TargetRef, p.Desired.Kind, short(p.Desired.Checksum))
	default:
		return fmt.Sprintf("%s [%s]: update — %s → %s", p.TargetRef, p.Desired.Kind, short(p.Current.Value), short(p.Desired.Checksum))
	}
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

// EvaluateRisk runs the blast-radius gate for a config + rollout-time inputs.
// Callers set ApplyRequest.NeedsApproval from the returned Decision.
func (e *Engine) EvaluateRisk(c *config.Config, in RiskInputs) (risk.Decision, error) {
	g := risk.Gate{
		Threshold:     c.Spec.Risk.Threshold,
		SensitiveExpr: c.Spec.Risk.Sensitive,
	}
	return g.Evaluate(risk.Signals{
		Criticality: c.Spec.Target.Criticality,
		Environment: in.Environment,
		ChangeType:  in.ChangeType,
		BlastRadius: in.BlastRadius,
		Strategy:    c.Spec.Strategy.Type,
	})
}

// ApplyRequest drives Apply.
type ApplyRequest struct {
	Config    *config.Config
	Initiator rollout.Identity
	// Planned records that a plan/diff was produced first. Agent-driven
	// rollouts must set this — an agent cannot apply blind.
	Planned bool
	// NeedsApproval is the risk gate's verdict; when true the rollout stops at
	// awaiting-approval instead of deploying. (Wired by the risk-gate task.)
	NeedsApproval bool
}

// Apply deploys the desired state to the target and persists the rollout,
// driving phases through the statekit lifecycle so every transition is legal.
// On target failure the rollout is recorded as rolled-back and the error
// returned; on success it advances to verifying. If the gate requires approval
// the rollout halts at awaiting-approval and the target is not touched.
func (e *Engine) Apply(ctx context.Context, req ApplyRequest) (*rollout.Rollout, error) {
	if req.Initiator.Kind == "agent" && !req.Planned {
		return nil, fmt.Errorf("engine: apply: agent-driven rollout requires a produced plan first")
	}
	release, ok := e.locks.TryAcquire(req.Config.Spec.Target.Ref)
	if !ok {
		return nil, ErrTargetBusy
	}
	defer release()

	m, err := manifestFromConfig(req.Config)
	if err != nil {
		return nil, err
	}
	tgt, err := e.build(req.Config.Spec.Target)
	if err != nil {
		return nil, err
	}

	lc, err := rollout.NewLifecycle(rollout.LifeContext{
		PlanProduced:  req.Planned || req.Initiator.Kind != "agent",
		NeedsApproval: req.NeedsApproval,
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
		TargetRef: req.Config.Spec.Target.Ref,
		Phase:     lc.Phase(), // deploying, or awaiting-approval if gated
		Strategy:  strategyFrom(req.Config),
		Desired:   m,
		Initiator: req.Initiator,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return nil, err
	}
	if r.Phase == rollout.PhaseAwaitingApproval {
		return &r, nil // halt: gate requires human approval, target untouched
	}

	if _, err := tgt.Apply(ctx, m); err != nil {
		_, _ = lc.Send(rollout.EventError)
		r.Phase = lc.Phase()
		r.UpdatedAt = e.now()
		_ = e.store.SaveRollout(ctx, r)
		return &r, fmt.Errorf("engine: apply: %w", err)
	}
	if _, err := lc.Send(rollout.EventDeployed); err != nil {
		return nil, err
	}
	r.Phase = lc.Phase()
	r.UpdatedAt = e.now()
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return nil, err
	}
	return &r, nil
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
	auto := c.Spec.Rollback.Auto

	failed, reason := e.runPostDeployChecks(ctx, r, c)
	if failed {
		if !auto {
			return VerifyOutcome{Rollout: r, Reason: reason}, fmt.Errorf("engine: verify failed (auto-rollback disabled): %s", reason)
		}
		rb, err := e.Rollback(ctx, rolloutID, prior)
		if err != nil {
			return VerifyOutcome{Rollout: r, Reason: reason}, fmt.Errorf("engine: auto-rollback after %q: %w", reason, err)
		}
		return VerifyOutcome{Rollout: rb, RolledBack: true, Reason: reason}, nil
	}
	promoted, err := e.Promote(ctx, rolloutID)
	if err != nil {
		return VerifyOutcome{}, err
	}
	return VerifyOutcome{Rollout: promoted}, nil
}

// runPostDeployChecks returns (failed, reason) for the health + smoke gates.
func (e *Engine) runPostDeployChecks(ctx context.Context, r rollout.Rollout, c *config.Config) (bool, string) {
	if hc := c.Spec.Rollback.HealthCheck; hc != nil || c.Spec.Rollback.Auto {
		tgt, err := e.buildTarget(r.TargetRef, r.Desired)
		if err == nil {
			if hs, herr := tgt.Health(ctx); herr != nil || hs.State == pt.HealthUnhealthy {
				reason := "health check failed"
				if hs.Reason != "" {
					reason = "health check failed: " + hs.Reason
				}
				return true, reason
			}
		}
	}
	if st := c.Spec.Rollback.SmokeTest; st != nil && len(st.Command) > 0 {
		code, err := e.smoke.Run(ctx, st.Command)
		if err != nil {
			return true, "smoke test error: " + err.Error()
		}
		if code != st.ExpectExit {
			return true, fmt.Sprintf("smoke test exit %d (expected %d)", code, st.ExpectExit)
		}
	}
	return false, ""
}

// Approve resolves an awaiting-approval rollout: it deploys to the target and
// advances to verifying. This is the human/agent "approve" arm of the single
// risk gate.
func (e *Engine) Approve(ctx context.Context, rolloutID string, by rollout.Identity) (rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return rollout.Rollout{}, err
	}
	release, ok := e.locks.TryAcquire(r.TargetRef)
	if !ok {
		return r, ErrTargetBusy
	}
	defer release()

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

// Verify gates promotion on the target's health. It does not change phase; a
// healthy verify clears the way for Promote, an unhealthy one is the auto-
// rollback signal the reconciler acts on.
func (e *Engine) Verify(ctx context.Context, rolloutID string) (rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return rollout.Rollout{}, err
	}
	tgt, err := e.buildTarget(r.TargetRef, r.Desired)
	if err != nil {
		return r, err
	}
	hs, err := tgt.Health(ctx)
	if err != nil {
		return r, fmt.Errorf("engine: verify: health: %w", err)
	}
	if hs.State != pt.HealthHealthy {
		return r, fmt.Errorf("engine: verify: target unhealthy (%s)", hs.Reason)
	}
	return r, nil
}

// Promote marks a verified rollout as promoted.
func (e *Engine) Promote(ctx context.Context, rolloutID string) (rollout.Rollout, error) {
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
	r.UpdatedAt = e.now()
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return rollout.Rollout{}, err
	}
	return r, nil
}

// Rollback re-applies a prior manifest to the target — the observability-free
// recovery path, driveable manually, by an agent, or automatically.
func (e *Engine) Rollback(ctx context.Context, rolloutID string, prior pt.Manifest) (rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return rollout.Rollout{}, err
	}
	release, ok := e.locks.TryAcquire(r.TargetRef)
	if !ok {
		return r, ErrTargetBusy
	}
	defer release()

	lc, err := rollout.ResumeLifecycle(r.Phase, rollout.LifeContext{PlanProduced: true})
	if err != nil {
		return rollout.Rollout{}, err
	}
	if _, err := lc.Send(rollout.EventRollback); err != nil {
		return r, err // not in a rollbackable phase
	}
	tgt, err := e.buildTarget(r.TargetRef, prior)
	if err != nil {
		return r, err
	}
	if _, err := tgt.Apply(ctx, prior); err != nil {
		return r, fmt.Errorf("engine: rollback: re-apply: %w", err)
	}
	r.Phase = lc.Phase()
	r.Desired = prior
	r.UpdatedAt = e.now()
	if err := e.store.SaveRollout(ctx, r); err != nil {
		return rollout.Rollout{}, err
	}
	return r, nil
}

// Status returns a rollout by id.
func (e *Engine) Status(ctx context.Context, id string) (rollout.Rollout, error) {
	return e.store.LoadRollout(ctx, id)
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
	release, ok := e.locks.TryAcquire(s.TargetRef)
	if !ok {
		return rollout.Rollout{}, ErrTargetBusy
	}
	defer release()

	tgt, err := e.buildTarget(s.TargetRef, s.Desired)
	if err != nil {
		return rollout.Rollout{}, err
	}
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

func manifestFromConfig(c *config.Config) (pt.Manifest, error) {
	spec, err := json.Marshal(c.Spec.Target.Spec)
	if err != nil {
		return pt.Manifest{}, fmt.Errorf("engine: marshal target spec: %w", err)
	}
	sum := sha256.Sum256(spec)
	return pt.Manifest{
		Kind:     c.Spec.Target.Kind,
		Spec:     spec,
		Labels:   c.Metadata.Labels,
		Checksum: hex.EncodeToString(sum[:]),
	}, nil
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
