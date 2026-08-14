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
		// An in-flight canary must keep ticking even when Git and the target
		// already match — Plan.Changed is false after the first Apply, but the
		// bake is not done.
		if inf, ok, err := r.eng.InFlight(ctx, c.Spec.Target.Ref); err != nil {
			return Outcome{}, err
		} else if ok {
			rl, err := r.eng.Tick(ctx, inf.ID, c)
			if err != nil {
				return Outcome{Plan: plan, Rollout: rl}, fmt.Errorf("reconcile: tick: %w", err)
			}
			return r.finalize(ctx, c, by, plan, false, rl)
		}
		// detect mode: live drift found but intentionally not auto-corrected —
		// record an alert so operators see it, then stop (no apply).
		if plan.DriftAlert {
			r.record(audit.Entry{
				Action:    audit.ActionDrift,
				TargetRef: c.Spec.Target.Ref,
				Actor:     by,
				Detail:    plan.Summary,
			})
			return Outcome{Drift: true, Plan: plan}, nil
		}
		return Outcome{Drift: false, Plan: plan}, nil
	}

	r.record(audit.Entry{
		Action:    audit.ActionDrift,
		TargetRef: c.Spec.Target.Ref,
		Actor:     by,
		Detail:    plan.Summary,
	})

	rl, err := r.eng.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: by, Planned: true, Risk: engine.RiskFromConfig(c)})
	if err != nil {
		return Outcome{Drift: true, Plan: plan, Rollout: rl}, fmt.Errorf("reconcile: apply: %w", err)
	}
	return r.finalize(ctx, c, by, plan, true, rl)
}

// finalize runs the post-deploy gate only once the stepper has reached
// verifying. deploying/paused/awaiting-approval halt this tick.
func (r *Reconciler) finalize(ctx context.Context, c *config.Config, by rollout.Identity, plan *engine.Plan, drifted bool, rl *rollout.Rollout) (Outcome, error) {
	if rl == nil {
		return Outcome{Drift: drifted, Plan: plan}, nil
	}
	switch rl.Phase {
	case rollout.PhaseAwaitingApproval, rollout.PhaseDeploying, rollout.PhasePaused:
		return Outcome{Drift: drifted, Plan: plan, Rollout: rl}, nil
	}

	prior := rl.Desired
	if p, ok := r.eng.PriorManifest(ctx, rl.TargetRef, rl.Desired.Checksum); ok {
		prior = p
	}
	out, err := r.eng.VerifyOrRollback(ctx, rl.ID, prior, c)
	if err != nil {
		return Outcome{Drift: drifted, Reconciled: true, Plan: plan, Rollout: rl}, fmt.Errorf("reconcile: verify: %w", err)
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
	return Outcome{Drift: drifted, Reconciled: true, Plan: plan, Rollout: &final}, nil
}

// waitingOn returns a dependsOn target ref that is not currently promoted, or
// "" if every dependency is promoted (or there are none). A missing rollout
// is not promoted. Errors from the store are returned so a lookup failure is
// not silently treated as "ready".
func (r *Reconciler) waitingOn(ctx context.Context, c *config.Config) (string, error) {
	for _, dep := range c.Spec.DependsOn {
		ok, err := r.isPromoted(ctx, dep)
		if err != nil {
			return "", err
		}
		if !ok {
			return dep, nil
		}
	}
	return "", nil
}

func (r *Reconciler) isPromoted(ctx context.Context, targetRef string) (bool, error) {
	rs, err := r.eng.List(ctx, 0)
	if err != nil {
		return false, err
	}
	for _, rl := range rs { // newest first
		if rl.TargetRef == targetRef {
			return rl.Phase == rollout.PhasePromoted, nil
		}
	}
	return false, nil
}

func (r *Reconciler) record(e audit.Entry) {
	if r.audit != nil {
		r.audit.Record(e)
	}
}
