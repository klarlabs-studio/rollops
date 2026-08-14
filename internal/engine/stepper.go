package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/progressive"
	"go.klarlabs.de/rollops/internal/rollout"
	pt "go.klarlabs.de/rollops/pkg/target"
	"go.klarlabs.de/statekit"
)

// stepperPersist is the opaque JSON stored on Rollout.StepperSnap.
type stepperPersist struct {
	Plan      progressive.Plan                           `json:"plan"`
	Snap      statekit.Snapshot[progressive.StepContext] `json:"snap"`
	EnteredAt time.Time                                  `json:"entered_at"`
}

// Tick advances an in-flight canary one due step. The target lease is
// re-acquired for this call and released when it returns; occupancy between
// ticks is the deploying/paused phase. A pause that has not elapsed is a
// no-op (still deploying). Operator-paused rollouts are not advanced (C2).
func (e *Engine) Tick(ctx context.Context, rolloutID string, cfg *config.Config) (*rollout.Rollout, error) {
	r, err := e.store.LoadRollout(ctx, rolloutID)
	if err != nil {
		return nil, err
	}
	switch r.Phase {
	case rollout.PhaseDeploying:
		// continue
	case rollout.PhasePaused:
		return &r, nil
	default:
		return &r, nil
	}
	if len(r.StepperSnap) == 0 {
		return &r, fmt.Errorf("engine: tick: rollout %s is deploying without a stepper snapshot", r.ID)
	}
	if cfg.Spec.Target.Ref != r.TargetRef {
		return &r, fmt.Errorf("engine: tick: config target %q does not match rollout %q", cfg.Spec.Target.Ref, r.TargetRef)
	}

	release, ok, err := e.acquireTarget(ctx, r.TargetRef)
	if err != nil {
		return &r, err
	}
	if !ok {
		return &r, ErrTargetBusy
	}
	defer release()

	tcfg, err := e.resolveSecrets(ctx, cfg.Spec.Target)
	if err != nil {
		return &r, err
	}
	tgt, err := e.build(tcfg)
	if err != nil {
		return &r, err
	}
	defer closeTarget(tgt)

	var persisted stepperPersist
	if err := json.Unmarshal(r.StepperSnap, &persisted); err != nil {
		return &r, fmt.Errorf("engine: tick: snapshot: %w", err)
	}
	var healthErr error
	health := e.stepHealth(ctx, tgt, &healthErr)
	machine, err := progressive.BuildStepMachine(persisted.Plan, health)
	if err != nil {
		return &r, err
	}
	persisted.Snap.PendingTimers = nil // re-arm After() on the FakeClock
	clk := statekit.NewFakeClock(e.now())
	s, err := progressive.RestoreStepper(machine, persisted.Snap, statekit.WithClock[progressive.StepContext](clk))
	if err != nil {
		return &r, fmt.Errorf("engine: tick: restore: %w", err)
	}

	lc, err := rollout.ResumeLifecycle(r.Phase, rollout.LifeContext{PlanProduced: true})
	if err != nil {
		s.Stop()
		return &r, err
	}

	if !s.Complete() && !s.Aborted() {
		idx := s.Context().Index
		if idx >= 0 && idx < len(persisted.Plan.Steps) {
			pause := persisted.Plan.Steps[idx].Pause
			if pause > 0 && e.now().Sub(persisted.EnteredAt) < pause {
				if !health(idx, s.Context().Weight) {
					s.Stop()
					return e.failProgressive(ctx, lc, &r, cfg, r.Desired, healthErr, r.Initiator)
				}
				s.Stop()
				return &r, nil
			}
		}
	}
	return e.driveStepper(ctx, lc, &r, cfg, persisted.Plan, s, clk, persisted.EnteredAt, &healthErr, r.Initiator)
}

// InFlight returns the newest deploying or paused rollout for targetRef.
func (e *Engine) InFlight(ctx context.Context, targetRef string) (rollout.Rollout, bool, error) {
	rs, err := e.store.ListRollouts(ctx, 0)
	if err != nil {
		return rollout.Rollout{}, false, err
	}
	for _, r := range rs {
		if r.TargetRef == targetRef && (r.Phase == rollout.PhaseDeploying || r.Phase == rollout.PhasePaused) {
			return r, true, nil
		}
	}
	return rollout.Rollout{}, false, nil
}

func (e *Engine) stepHealth(ctx context.Context, tgt pt.Target, healthErr *error) progressive.StepHealth {
	return func(stepIndex, weight int) bool {
		hs, herr := tgt.Health(ctx)
		if herr != nil {
			*healthErr = herr
			return false
		}
		if hs.State == pt.HealthUnhealthy {
			*healthErr = fmt.Errorf("unhealthy: %s", hs.Reason)
			return false
		}
		return true
	}
}

func (e *Engine) deployOnce(ctx context.Context, cfg *config.Config, tgt pt.Target, m pt.Manifest) error {
	if mig := cfg.Spec.DatabaseMigrate(); mig != nil && cfg.Spec.DatabaseMigrateWhen() == config.MigratePreDeploy {
		if err := e.runDatabaseCommand(ctx, mig); err != nil {
			return fmt.Errorf("database migrate: %w", err)
		}
	}
	_, err := tgt.Apply(ctx, m)
	return err
}

func (e *Engine) startStepper(ctx context.Context, lc *rollout.Lifecycle, r *rollout.Rollout, cfg *config.Config, tgt pt.Target, actor rollout.Identity) (*rollout.Rollout, error) {
	plan := progressive.PlanFor(cfg.Spec.Strategy)
	var healthErr error
	health := e.stepHealth(ctx, tgt, &healthErr)
	machine, err := progressive.BuildStepMachine(plan, health)
	if err != nil {
		return e.failProgressive(ctx, lc, r, cfg, r.Desired, err, actor)
	}
	clk := statekit.NewFakeClock(e.now())
	s := progressive.NewStepper(machine, statekit.WithClock[progressive.StepContext](clk))
	return e.driveStepper(ctx, lc, r, cfg, plan, s, clk, e.now(), &healthErr, actor)
}

func (e *Engine) driveStepper(ctx context.Context, lc *rollout.Lifecycle, r *rollout.Rollout, cfg *config.Config, plan progressive.Plan, s *progressive.Stepper, clk *statekit.FakeClock, enteredAt time.Time, healthErr *error, actor rollout.Identity) (*rollout.Rollout, error) {
	defer s.Stop()

	recordStep := func() error {
		cur := s.Context()
		i, total, w := cur.Index+1, len(plan.Steps), cur.Weight
		if r.StepIndex == i && r.StepWeight == w {
			return nil // already persisted this step (Tick restore)
		}
		r.StepIndex, r.StepTotal, r.StepWeight = i, total, w
		r.Note = fmt.Sprintf("%s step %d/%d (%d%%) passed health gate", plan.Strategy, i, total, w)
		r.UpdatedAt = e.now()
		_ = e.store.SaveRollout(ctx, *r)
		r.Note = ""
		if cfg.Spec.TrafficRouting != nil {
			if err := e.driveTraffic(ctx, r.TargetRef, cfg.Spec.TrafficRouting, w); err != nil {
				return err
			}
		}
		if flagsEnabled(cfg.Spec.FeatureFlags, "step") {
			e.driveFlag(ctx, r.TargetRef, cfg.Spec.FeatureFlags, w)
		}
		return nil
	}

	if s.Aborted() {
		err := *healthErr
		if err == nil {
			err = fmt.Errorf("progressive: %s aborted", plan.Strategy)
		}
		return e.failProgressive(ctx, lc, r, cfg, r.Desired, err, actor)
	}
	if err := recordStep(); err != nil {
		return e.failProgressive(ctx, lc, r, cfg, r.Desired, err, actor)
	}

	for !s.Complete() && !s.Aborted() {
		idx := s.Context().Index
		if idx < 0 || idx >= len(plan.Steps) {
			break
		}
		pause := plan.Steps[idx].Pause
		if pause > 0 && e.now().Sub(enteredAt) < pause {
			break
		}
		if pause == 0 {
			s.Next()
		} else {
			clk.Advance(pause)
		}
		if s.Aborted() {
			err := *healthErr
			if err == nil {
				err = fmt.Errorf("progressive: %s step %d/%d health gate failed", plan.Strategy, idx+1, len(plan.Steps))
			}
			return e.failProgressive(ctx, lc, r, cfg, r.Desired, err, actor)
		}
		if s.Complete() || s.Context().Index != idx {
			enteredAt = e.now()
			if !s.Complete() {
				if err := recordStep(); err != nil {
					return e.failProgressive(ctx, lc, r, cfg, r.Desired, err, actor)
				}
			}
			continue
		}
		break
	}

	if s.Aborted() {
		err := *healthErr
		if err == nil {
			err = fmt.Errorf("progressive: %s aborted", plan.Strategy)
		}
		return e.failProgressive(ctx, lc, r, cfg, r.Desired, err, actor)
	}

	snap := s.Snapshot()
	blob, err := json.Marshal(stepperPersist{Plan: plan, Snap: snap, EnteredAt: enteredAt})
	if err != nil {
		return nil, fmt.Errorf("engine: stepper snapshot: %w", err)
	}
	r.StepperSnap = blob
	r.UpdatedAt = e.now()

	if s.Complete() {
		if _, err := lc.Send(rollout.EventDeployed); err != nil {
			return nil, err
		}
		r.Phase = lc.Phase()
		if err := e.store.SaveRollout(ctx, *r); err != nil {
			return nil, err
		}
		e.record(audit.Entry{Action: audit.ActionApply, RolloutID: r.ID, TargetRef: r.TargetRef, Phase: string(r.Phase), Actor: actor, Detail: "deployed (" + plan.Strategy + ")"})
		return r, nil
	}

	r.Phase = lc.Phase()
	if err := e.store.SaveRollout(ctx, *r); err != nil {
		return nil, err
	}
	return r, nil
}

// failProgressive is the deploy-step failure path: auto-rollback to a prior
// good manifest when configured, otherwise mark rolled-back. The caller MUST
// already hold the target lock (Apply and Tick both do).
func (e *Engine) failProgressive(ctx context.Context, lc *rollout.Lifecycle, r *rollout.Rollout, cfg *config.Config, m pt.Manifest, runErr error, actor rollout.Identity) (*rollout.Rollout, error) {
	if cfg.Spec.Rollback.Auto {
		if prior, ok := e.priorManifest(ctx, r.TargetRef, m.Checksum); ok {
			prior.Root = m.Root
			rb, rbErr := e.applyRollback(ctx, r, prior, "auto-rollback on deploy failure: "+runErr.Error(), cfg.Spec.DatabaseRollbackHook(), true)
			if rbErr == nil {
				e.record(audit.Entry{Action: audit.ActionRollback, RolloutID: rb.ID, TargetRef: r.TargetRef, Phase: string(rb.Phase), Actor: actor, Detail: "auto-rollback: " + runErr.Error()})
				e.notifyDeployment(ctx, notify.RolledBack, rb, runErr.Error())
				return &rb, fmt.Errorf("engine: apply: %w (auto-rolled back to prior manifest)", runErr)
			}
		}
	}
	_, _ = lc.Send(rollout.EventError)
	r.Phase = lc.Phase()
	r.UpdatedAt = e.now()
	_ = e.store.SaveRollout(ctx, *r)
	e.record(audit.Entry{Action: audit.ActionRollback, RolloutID: r.ID, TargetRef: r.TargetRef, Phase: string(r.Phase), Actor: actor, Detail: runErr.Error()})
	e.notifyDeployment(ctx, notify.Failed, *r, runErr.Error())
	return r, fmt.Errorf("engine: apply: %w", runErr)
}
