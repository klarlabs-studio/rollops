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
	"fmt"
	"time"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/store"
	itarget "go.klarlabs.de/rolloffs/internal/target"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// Engine orchestrates rollouts over a Store and a target Registry.
type Engine struct {
	store store.Store
	reg   *itarget.Registry
	now   func() time.Time
	newID func() string
}

// Option configures an Engine.
type Option func(*Engine)

// WithClock overrides the time source (deterministic tests).
func WithClock(f func() time.Time) Option { return func(e *Engine) { e.now = f } }

// WithIDGen overrides the rollout id generator (deterministic tests).
func WithIDGen(f func() string) Option { return func(e *Engine) { e.newID = f } }

// New builds an Engine. By default ids are time-derived and the clock is
// time.Now; both are injectable for tests.
func New(st store.Store, reg *itarget.Registry, opts ...Option) *Engine {
	e := &Engine{
		store: st,
		reg:   reg,
		now:   func() time.Time { return time.Now().UTC() },
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
	tgt, err := e.reg.Build(c.Spec.Target)
	if err != nil {
		return nil, err
	}
	cur, err := tgt.Observe(ctx)
	if err != nil {
		return nil, fmt.Errorf("engine: plan: observe: %w", err)
	}
	return &Plan{
		TargetRef: c.Spec.Target.Ref,
		Desired:   m,
		Current:   cur,
		Changed:   cur.Value != m.Checksum,
	}, nil
}

// Plan is the result of Plan — surfaced to humans and agents before apply.
type Plan struct {
	TargetRef string
	Desired   pt.Manifest
	Current   pt.Fingerprint
	Changed   bool
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
	m, err := manifestFromConfig(req.Config)
	if err != nil {
		return nil, err
	}
	tgt, err := e.reg.Build(req.Config.Spec.Target)
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

// Observe queries the target's live fingerprint and records it for drift
// detection. Returns the fingerprint observed.
func (e *Engine) Observe(ctx context.Context, t config.Target) (pt.Fingerprint, error) {
	tgt, err := e.reg.Build(t)
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

// Schedule queues a rollout for a future time. A blank id is assigned.
func (e *Engine) Schedule(ctx context.Context, s rollout.ScheduledRollout) error {
	if s.ID == "" {
		s.ID = "sch-" + e.newID()
	}
	return e.store.Schedule(ctx, s)
}

// buildTarget reconstructs a bound Target from a persisted manifest.
func (e *Engine) buildTarget(ref string, m pt.Manifest) (pt.Target, error) {
	var spec map[string]any
	if len(m.Spec) > 0 {
		if err := json.Unmarshal(m.Spec, &spec); err != nil {
			return nil, fmt.Errorf("engine: decode manifest spec: %w", err)
		}
	}
	return e.reg.Build(config.Target{Kind: m.Kind, Ref: ref, Spec: spec})
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
