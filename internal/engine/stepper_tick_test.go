package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const canaryPauseYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: medium
    spec:
      x: 1
  strategy:
    type: canary
    steps:
      - weight: 10
        pause: 2s
      - weight: 100
        pause: 2s
`

func loadCanaryPause(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load([]byte(canaryPauseYAML))
	if err != nil {
		t.Fatalf("load canary config: %v", err)
	}
	return c
}

// TestApply_CanaryPauseCompletesAcrossTwoTicks is C1 acceptance: a canary with
// pause: 2s must not block inside Apply. It finishes across two Tick calls.
func TestApply_CanaryPauseCompletesAcrossTwoTicks(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, db := newEngine(t, fake, WithClock(func() time.Time { return now }))
	ctx := context.Background()
	cfg := loadCanaryPause(t)

	start := time.Now()
	r, err := e.Apply(ctx, ApplyRequest{Config: cfg, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// The bound is deliberately far below the 2s pause and far above what
	// Apply costs (~5ms, and ~350ms on a machine under heavy parallel load).
	// It used to be the pause itself, 50ms, which could not tell "slept
	// through the pause" apart from "did 50ms of honest work" — so it failed
	// on a loaded machine while reporting a sleep that the engine cannot
	// perform. Nothing in the engine sleeps; the pause is evaluated as
	// now().Sub(enteredAt) against the injected clock, which does not advance
	// during Apply. Widening the gap is what makes this assertion able to
	// fail for the reason its message gives.
	if elapsed := time.Since(start); elapsed >= time.Second {
		t.Fatalf("Apply blocked for %s; canary pause must not sleep inside Apply", elapsed)
	}
	if r.Phase != rollout.PhaseDeploying {
		t.Fatalf("phase after Apply = %q, want deploying (still baking)", r.Phase)
	}
	if len(fake.applied) != 1 {
		t.Fatalf("target applied %d times, want 1 (deploy once)", len(fake.applied))
	}
	if r.StepIndex != 1 || r.StepWeight != 10 {
		t.Errorf("after Apply step = %d/%d (%d%%), want 1/? (10%%)", r.StepIndex, r.StepTotal, r.StepWeight)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatalf("LoadRollout: %v", err)
	}
	if len(got.StepperSnap) == 0 {
		t.Fatal("Apply must persist a stepper snapshot so a restart can resume")
	}

	now = now.Add(2 * time.Second)
	r, err = e.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick 1: %v", err)
	}
	if r.Phase != rollout.PhaseDeploying {
		t.Fatalf("phase after Tick 1 = %q, want deploying", r.Phase)
	}
	if r.StepWeight != 100 {
		t.Errorf("after Tick 1 weight = %d, want 100", r.StepWeight)
	}

	now = now.Add(2 * time.Second)
	r, err = e.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick 2: %v", err)
	}
	if r.Phase != rollout.PhaseVerifying {
		t.Fatalf("phase after Tick 2 = %q, want verifying", r.Phase)
	}
	if len(fake.applied) != 1 {
		t.Fatalf("target applied %d times after ticks, want 1", len(fake.applied))
	}
}

// TestTick_RestartMidPauseResumesFromSnapshot proves crash recovery: a new
// Engine against the same Store restores the snapshot and continues.
func TestTick_RestartMidPauseResumesFromSnapshot(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	clock := func() time.Time { return now }
	e1 := New(db, reg, WithClock(clock), WithIDGen(func() string { return "ro-restart" }))
	ctx := context.Background()
	cfg := loadCanaryPause(t)

	r, err := e1.Apply(ctx, ApplyRequest{Config: cfg, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.Phase != rollout.PhaseDeploying {
		t.Fatalf("phase = %q, want deploying", r.Phase)
	}

	e2 := New(db, reg, WithClock(clock), WithIDGen(func() string { return "ro-restart-2" }))
	now = now.Add(2 * time.Second)
	r, err = e2.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick after restart: %v", err)
	}
	if r.Phase != rollout.PhaseDeploying || r.StepWeight != 100 {
		t.Fatalf("after restart Tick 1: phase=%s weight=%d, want deploying/100", r.Phase, r.StepWeight)
	}
	now = now.Add(2 * time.Second)
	r, err = e2.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick 2 after restart: %v", err)
	}
	if r.Phase != rollout.PhaseVerifying {
		t.Fatalf("phase after restart Tick 2 = %q, want verifying", r.Phase)
	}
	if len(fake.applied) != 1 {
		t.Fatalf("applied %d times, want 1", len(fake.applied))
	}
}

func TestApply_InFlightCanaryIsBusy(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }), WithIDGen(incIDs()))
	ctx := context.Background()
	cfg := loadCanaryPause(t)

	if _, err := e.Apply(ctx, ApplyRequest{Config: cfg}); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	_, err := e.Apply(ctx, ApplyRequest{Config: cfg})
	if err == nil {
		t.Fatal("second Apply during an in-flight canary must be busy")
	}
}

func TestTick_BeforePauseElapsesIsNoop(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }))
	ctx := context.Background()
	cfg := loadCanaryPause(t)

	r, err := e.Apply(ctx, ApplyRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r2, err := e.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if r2.Phase != rollout.PhaseDeploying || r2.StepWeight != 10 {
		t.Fatalf("early Tick advanced the canary: phase=%s weight=%d", r2.Phase, r2.StepWeight)
	}
}

// A canary whose health fails at a step it entered from an elapsed pause sat
// in deploying forever: the entry gate recorded Failed, the eventless abort
// did not fire for a step entered from a restored timer, and the step's
// "ok"-guarded timer never fired either. Every tick that arrived after the
// pause had elapsed hit the same wall.
func TestTick_UnhealthyAtAStepEnteredFromTheTimerFails(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }), WithIDGen(incIDs()))
	ctx := context.Background()
	c := loadCanaryPause(t)
	r, err := e.Apply(ctx, ApplyRequest{Config: c, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil || r.Phase != rollout.PhaseDeploying {
		t.Fatalf("apply: %v %v", r, err)
	}
	fake.health = pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "CrashLoopBackOff"}
	var last *rollout.Rollout
	for i := 0; i < 5; i++ {
		now = now.Add(3 * time.Second) // every tick lands after the step's pause
		last, _ = e.Tick(ctx, r.ID, c)
		if last.Phase != rollout.PhaseDeploying {
			break
		}
	}
	if last.Phase != rollout.PhaseRolledBack {
		t.Fatalf("an unhealthy canary is still %s after 5 ticks (step %d)", last.Phase, last.StepIndex)
	}
}

// A rollout that is deploying with no stepper snapshot was interrupted before
// it recorded any progress. Ticking it must resolve it, not refuse it.
//
// This is how rollops' own daemon wedged the first time it deployed itself.
// The rolling path saves the rollout as deploying, applies, then saves it as
// verifying; the apply replaced the daemon's own pod, so the second save never
// happened. The row stayed deploying forever, which counts as in flight, so
// every Apply was refused as busy and every Tick was refused as
// "deploying without a stepper snapshot" — an engine that would neither
// advance the rollout nor let anything else near the target.
func TestTick_InterruptedRolloutIsResolvedNotRefused(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	c := loadConfig(t)
	ctx := context.Background()

	if err := db.SaveRollout(ctx, rollout.Rollout{
		ID:        "ro-interrupted",
		TargetRef: c.Spec.Target.Ref,
		Phase:     rollout.PhaseDeploying,
		Strategy:  rollout.StrategyRolling,
		// No StepperSnap: the process died before writing one.
	}); err != nil {
		t.Fatal(err)
	}

	got, err := e.Tick(ctx, "ro-interrupted", c)
	if err != nil {
		t.Fatalf("Tick on an interrupted rollout: %v", err)
	}
	if got.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q, want verifying — the post-deploy gate decides what actually landed", got.Phase)
	}
	if got.RollbackBlocked == "" {
		t.Error("rollback must be blocked: what reached the target is unknown, so the recorded prior is not a verified baseline")
	}

	// The target must be free again: verifying is not in flight, so the next
	// reconcile can converge forward.
	if _, inFlight, err := e.InFlight(ctx, c.Spec.Target.Ref); err != nil {
		t.Fatal(err)
	} else if inFlight {
		t.Error("target still reports a rollout in flight: Apply would keep being refused as busy")
	}

	// Persisted, not just returned — the next process must see the same thing.
	reloaded, err := db.LoadRollout(ctx, "ro-interrupted")
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Phase != rollout.PhaseVerifying || reloaded.RollbackBlocked == "" {
		t.Errorf("persisted rollout = phase %q, blocked %q", reloaded.Phase, reloaded.RollbackBlocked)
	}
}
