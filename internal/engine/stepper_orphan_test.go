package engine

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/rollout"
)

// crashAfterDeploy reproduces the durable state that a process death between
// Apply's phase write (engine.go, Phase=deploying) and driveStepper's snapshot
// write leaves behind: deploying, with no stepper snapshot. Nothing in the
// engine can produce this in-process, so the test writes it directly.
func crashAfterDeploy(t *testing.T, e *Engine, id string) {
	t.Helper()
	ctx := context.Background()
	r, err := e.store.LoadRollout(ctx, id)
	if err != nil {
		t.Fatalf("LoadRollout: %v", err)
	}
	r.StepperSnap = nil
	if err := e.store.SaveRollout(ctx, r); err != nil {
		t.Fatalf("SaveRollout: %v", err)
	}
}

// TestTick_OrphanedDeployRecoversInsteadOfWedging is the regression test for
// the month-long production wedge: four targets sat in deploying with no
// snapshot, Tick errored on every reconcile, and because occupancy IS the
// deploying phase the target was never released — every later rollout for it
// failed with ErrTargetBusy, forever. Tick must recover the orphan, not
// diagnose it in a loop.
func TestTick_OrphanedDeployRecoversInsteadOfWedging(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, db := newEngine(t, fake, WithClock(func() time.Time { return now }))
	ctx := context.Background()
	cfg := loadCanaryPause(t)

	r, err := e.Apply(ctx, ApplyRequest{Config: cfg, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	crashAfterDeploy(t, e, r.ID)

	got, err := e.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick on orphaned deploy must recover, got error: %v", err)
	}
	if got.Phase != rollout.PhaseDeploying && got.Phase != rollout.PhaseVerifying {
		t.Fatalf("phase after recovery = %q, want deploying or verifying", got.Phase)
	}

	stored, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatalf("LoadRollout: %v", err)
	}
	if len(stored.StepperSnap) == 0 && stored.Phase == rollout.PhaseDeploying {
		t.Fatal("recovery left the rollout deploying with no snapshot — still wedged")
	}

	// The desired manifest is re-applied, because deployOnce may have been the
	// very thing the crash interrupted. Apply is idempotent; assuming the
	// cluster already converged is how a recovery silently deploys nothing.
	if len(fake.applied) != 2 {
		t.Errorf("target applied %d times, want 2 (original + recovery re-apply)", len(fake.applied))
	}
}

// TestTick_OrphanedDeployDoesNotStealALiveApply guards the recovery's premise.
// Recovery is only safe because holding the target lease proves no Apply is
// mid-flight. If the lease is held elsewhere Tick must back off with
// ErrTargetBusy rather than re-applying underneath the running deploy.
func TestTick_OrphanedDeployDoesNotStealALiveApply(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }))
	ctx := context.Background()
	cfg := loadCanaryPause(t)

	r, err := e.Apply(ctx, ApplyRequest{Config: cfg, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	crashAfterDeploy(t, e, r.ID)
	applied := len(fake.applied)

	release, ok, err := e.acquireTarget(ctx, r.TargetRef)
	if err != nil || !ok {
		t.Fatalf("acquireTarget for the test's own hold: ok=%v err=%v", ok, err)
	}
	defer release()

	if _, err := e.Tick(ctx, r.ID, cfg); !errors.Is(err, ErrTargetBusy) {
		t.Fatalf("Tick with the lease held = %v, want ErrTargetBusy", err)
	}
	if len(fake.applied) != applied {
		t.Errorf("Tick re-applied while another holder had the lease (%d -> %d)", applied, len(fake.applied))
	}
}

// TestTick_OrphanRecoveryIsAudited keeps the recovery visible. Silent
// self-healing of a state that should be unreachable is how the next window
// like this one goes unnoticed for another month.
func TestTick_OrphanRecoveryIsAudited(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	var auditLog bytes.Buffer
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }), WithAudit(audit.New(&auditLog)))
	ctx := context.Background()
	cfg := loadCanaryPause(t)

	r, err := e.Apply(ctx, ApplyRequest{Config: cfg, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	crashAfterDeploy(t, e, r.ID)
	if _, err := e.Tick(ctx, r.ID, cfg); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if !strings.Contains(auditLog.String(), "recovered") {
		t.Fatalf("orphan recovery produced no audit entry mentioning 'recovered'; log was:\n%s", auditLog.String())
	}
}
