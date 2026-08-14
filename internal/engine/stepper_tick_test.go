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
        pause: 50ms
      - weight: 100
        pause: 50ms
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
// pause: 50ms must not block inside Apply. It finishes across two Tick calls.
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
	if time.Since(start) >= 50*time.Millisecond {
		t.Fatalf("Apply blocked for %s; canary pause must not sleep inside Apply", time.Since(start))
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

	now = now.Add(50 * time.Millisecond)
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

	now = now.Add(50 * time.Millisecond)
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
	now = now.Add(50 * time.Millisecond)
	r, err = e2.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick after restart: %v", err)
	}
	if r.Phase != rollout.PhaseDeploying || r.StepWeight != 100 {
		t.Fatalf("after restart Tick 1: phase=%s weight=%d, want deploying/100", r.Phase, r.StepWeight)
	}
	now = now.Add(50 * time.Millisecond)
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
