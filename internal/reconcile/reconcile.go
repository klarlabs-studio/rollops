// Package reconcile is the reconcile loop: read desired state, observe the
// target, diff, and on drift detect → alert → reconcile back to desired, subject
// to the risk gate. Drift = desired fingerprint != observed fingerprint. The
// poll tick that drives this also doubles as the drift heartbeat.
package reconcile

import (
	"context"
	"fmt"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
)

// Reconciler diffs desired vs observed and reconciles drift through the engine.
type Reconciler struct {
	eng   *engine.Engine
	audit *audit.Logger
}

// New builds a Reconciler. The audit logger may be nil (no audit).
func New(eng *engine.Engine, a *audit.Logger) *Reconciler {
	return &Reconciler{eng: eng, audit: a}
}

// Outcome reports what a reconcile did.
type Outcome struct {
	Drift      bool
	Reconciled bool
	Plan       *engine.Plan
	Rollout    *rollout.Rollout
}

// Reconcile observes the target, detects drift, and — if drifted — alerts and
// reconciles back to desired. In sync, it is a cheap no-op (the heartbeat).
func (r *Reconciler) Reconcile(ctx context.Context, c *config.Config, by rollout.Identity) (Outcome, error) {
	plan, err := r.eng.Plan(ctx, c)
	if err != nil {
		return Outcome{}, fmt.Errorf("reconcile: plan: %w", err)
	}
	if !plan.Changed {
		return Outcome{Drift: false, Plan: plan}, nil
	}

	r.record(audit.Entry{
		Action:    audit.ActionDrift,
		TargetRef: c.Spec.Target.Ref,
		Actor:     by,
		Detail:    plan.Summary,
	})

	rl, err := r.eng.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: by, Planned: true})
	if err != nil {
		return Outcome{Drift: true, Plan: plan, Rollout: rl}, fmt.Errorf("reconcile: apply: %w", err)
	}

	// Halted at the approval gate — nothing more to do this tick.
	if rl.Phase == rollout.PhaseAwaitingApproval {
		return Outcome{Drift: true, Plan: plan, Rollout: rl}, nil
	}

	// Finalize: post-deploy health/smoke gate promotes or auto-rolls-back.
	out, err := r.eng.VerifyOrRollback(ctx, rl.ID, rl.Desired, c)
	if err != nil {
		return Outcome{Drift: true, Reconciled: true, Plan: plan, Rollout: rl}, fmt.Errorf("reconcile: verify: %w", err)
	}
	final := out.Rollout
	r.record(audit.Entry{
		Action:    audit.ActionApply,
		RolloutID: final.ID,
		TargetRef: final.TargetRef,
		Phase:     string(final.Phase),
		Actor:     by,
		Detail:    "reconciled drift",
	})
	return Outcome{Drift: true, Reconciled: true, Plan: plan, Rollout: &final}, nil
}

func (r *Reconciler) record(e audit.Entry) {
	if r.audit != nil {
		r.audit.Record(e)
	}
}
