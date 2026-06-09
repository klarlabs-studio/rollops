package rollout

import (
	"fmt"

	"go.klarlabs.de/statekit"
)

// Lifecycle is the rollout statechart, modeled in statekit. It is the single
// source of truth for which phase transitions are legal and what guards them —
// the engine drives phase changes through it rather than mutating Phase ad hoc,
// so an illegal transition is rejected loudly and every legal one is explicit.
//
//	pending → validating → [GATE] → deploying → verifying → promoted
//	                          │                                 │
//	                          ▼ (needs approval)                ▼ (fail)
//	                   awaiting-approval ──approve──┘       rolled-back
//
// A rollout can be rolled back from awaiting-approval, deploying, or verifying
// (manual, agent, or the observability-free auto signals).
type Lifecycle struct {
	interp *statekit.Interpreter[LifeContext]
}

// LifeContext carries the guard inputs for the statechart. It is observability-
// free: every field is known without metrics.
type LifeContext struct {
	PlanProduced  bool // a plan/diff was computed before apply (required for agent-driven rollouts)
	NeedsApproval bool // risk gate said above-threshold or sensitive-flagged
}

// Lifecycle event names.
const (
	EventValidate = "VALIDATE"  // pending -> validating
	EventGate     = "GATE"      // validating -> deploying | awaiting-approval
	EventApprove  = "APPROVE"   // awaiting-approval -> deploying
	EventReject   = "REJECT"    // awaiting-approval -> rolled-back
	EventDeployed = "DEPLOYED"  // deploying -> verifying
	EventVerifyOK = "VERIFY_OK" // verifying -> promoted
	EventRollback = "ROLLBACK"  // deploying|verifying|awaiting-approval -> rolled-back
	EventError    = "ERROR"     // validating|deploying -> rolled-back
)

const (
	guardReadyToDeploy = "readyToDeploy" // !NeedsApproval && PlanProduced
	guardNeedsApproval = "needsApproval" // NeedsApproval
	guardCanDeploy     = "canDeploy"     // PlanProduced (post-approval)
)

func buildMachine(initial Phase) (*statekit.Interpreter[LifeContext], error) {
	m, err := statekit.NewMachine[LifeContext]("rollout").
		WithInitial(statekit.StateID(initial)).
		WithGuard(guardReadyToDeploy, func(c LifeContext, _ statekit.Event) bool {
			return !c.NeedsApproval && c.PlanProduced
		}).
		WithGuard(guardNeedsApproval, func(c LifeContext, _ statekit.Event) bool {
			return c.NeedsApproval
		}).
		WithGuard(guardCanDeploy, func(c LifeContext, _ statekit.Event) bool {
			return c.PlanProduced
		}).
		State(statekit.StateID(PhasePending)).
		On(EventValidate).Target(statekit.StateID(PhaseValidating)).Done().
		State(statekit.StateID(PhaseValidating)).
		On(EventGate).Target(statekit.StateID(PhaseDeploying)).Guard(guardReadyToDeploy).
		On(EventGate).Target(statekit.StateID(PhaseAwaitingApproval)).Guard(guardNeedsApproval).
		On(EventError).Target(statekit.StateID(PhaseRolledBack)).Done().
		State(statekit.StateID(PhaseAwaitingApproval)).
		On(EventApprove).Target(statekit.StateID(PhaseDeploying)).Guard(guardCanDeploy).
		On(EventReject).Target(statekit.StateID(PhaseRolledBack)).
		On(EventRollback).Target(statekit.StateID(PhaseRolledBack)).Done().
		State(statekit.StateID(PhaseDeploying)).
		On(EventDeployed).Target(statekit.StateID(PhaseVerifying)).
		On(EventError).Target(statekit.StateID(PhaseRolledBack)).
		On(EventRollback).Target(statekit.StateID(PhaseRolledBack)).Done().
		State(statekit.StateID(PhaseVerifying)).
		On(EventVerifyOK).Target(statekit.StateID(PhasePromoted)).
		On(EventRollback).Target(statekit.StateID(PhaseRolledBack)).Done().
		// promoted is the steady state, but a completed deploy can still be
		// reverted (GitOps rollback) → rolled-back.
		State(statekit.StateID(PhasePromoted)).
		On(EventRollback).Target(statekit.StateID(PhaseRolledBack)).Done().
		State(statekit.StateID(PhaseRolledBack)).Final().Done().
		Build()
	if err != nil {
		return nil, fmt.Errorf("rollout: build statechart: %w", err)
	}
	interp := statekit.NewInterpreter(m)
	return interp, nil
}

// NewLifecycle starts a fresh rollout statechart in the pending phase.
func NewLifecycle(ctx LifeContext) (*Lifecycle, error) {
	return resumeLifecycle(PhasePending, ctx)
}

// ResumeLifecycle reconstructs the statechart at an existing phase, for a
// rollout loaded from the Store mid-flight.
func ResumeLifecycle(at Phase, ctx LifeContext) (*Lifecycle, error) {
	return resumeLifecycle(at, ctx)
}

func resumeLifecycle(at Phase, ctx LifeContext) (*Lifecycle, error) {
	interp, err := buildMachine(at)
	if err != nil {
		return nil, err
	}
	interp.UpdateContext(func(c *LifeContext) { *c = ctx })
	interp.Start()
	return &Lifecycle{interp: interp}, nil
}

// Phase returns the current phase.
func (l *Lifecycle) Phase() Phase {
	return Phase(l.interp.State().Value)
}

// Send fires an event and returns the resulting phase. It errors if the event
// produced no transition from the current phase (illegal transition or a guard
// that did not pass), so callers never silently no-op.
func (l *Lifecycle) Send(event string) (Phase, error) {
	before := l.Phase()
	l.interp.Send(statekit.Event{Type: statekit.EventType(event)})
	after := l.Phase()
	if before == after {
		return after, fmt.Errorf("rollout: illegal transition: event %q rejected in phase %q", event, before)
	}
	return after, nil
}
