package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
)

// A deploying rollout without a snapshot is also what an Apply looks like
// while it is still running (it saves deploying, then waits on the rollout
// before the first snapshot). Recovery must not hijack it: it runs only
// once the target lease is free, which proves no Apply is mid-flight.
func TestTick_DoesNotRecoverARolloutWhoseApplyHoldsTheLease(t *testing.T) {
	e, db := newEngine(t, &fakeTarget{})
	c := loadConfig(t)
	ctx := context.Background()
	if err := db.SaveRollout(ctx, rollout.Rollout{ID: "ro-live", TargetRef: c.Spec.Target.Ref, Phase: rollout.PhaseDeploying, Strategy: rollout.StrategyRolling}); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.AcquireLease(ctx, "target:"+c.Spec.Target.Ref, "another-apply", time.Hour, e.now()); err != nil || !ok {
		t.Fatalf("AcquireLease = %v, %v", ok, err)
	}
	if _, err := e.Tick(ctx, "ro-live", c); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("Tick = %v; want ErrTargetBusy while another Apply holds the target", err)
	}
	got, _ := db.LoadRollout(ctx, "ro-live")
	if got.Phase != rollout.PhaseDeploying {
		t.Fatalf("phase = %q; the running Apply's rollout must be left alone", got.Phase)
	}
}

// A snapshot that cannot be decoded or restored (corrupt, or from an
// incompatible version) failed every Tick and kept the target busy: the
// 2026-09-19 wedge by another route. Progress is unknown either way, so it
// takes the same path as a missing snapshot.
func TestTick_UnrestorableSnapshotIsRecoveredNotRefused(t *testing.T) {
	e, db := newEngine(t, &fakeTarget{})
	c := loadConfig(t)
	ctx := context.Background()
	if err := db.SaveRollout(ctx, rollout.Rollout{
		ID: "ro-corrupt", TargetRef: c.Spec.Target.Ref, Phase: rollout.PhaseDeploying,
		Strategy: rollout.StrategyRolling, StepperSnap: []byte("{not json"),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Tick(ctx, "ro-corrupt", c)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got.Phase != rollout.PhaseVerifying || got.RollbackBlocked == "" {
		t.Fatalf("rollout = phase %q, blocked %q; want verifying with rollback blocked", got.Phase, got.RollbackBlocked)
	}
}
