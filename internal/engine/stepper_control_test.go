package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/rollout"
)

func TestPause_HoldsCanaryAcrossElapsedBake(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }))
	ctx := context.Background()
	cfg := loadCanaryPause(t)
	by := rollout.Identity{Kind: "human", Name: "felix"}

	applied, err := e.Apply(ctx, ApplyRequest{Config: cfg, Initiator: by})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r, err := e.Pause(ctx, applied.ID, by)
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if r.Phase != rollout.PhasePaused {
		t.Fatalf("phase after Pause = %q, want paused", r.Phase)
	}

	now = now.Add(2 * time.Second)
	ticked, err := e.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick while paused: %v", err)
	}
	if ticked.Phase != rollout.PhasePaused || ticked.StepWeight != 10 {
		t.Fatalf("paused Tick advanced the canary: phase=%s weight=%d", ticked.Phase, ticked.StepWeight)
	}
}

func TestResume_RestartsCurrentStepBake(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }))
	ctx := context.Background()
	cfg := loadCanaryPause(t)
	by := rollout.Identity{Kind: "human", Name: "felix"}

	applied, err := e.Apply(ctx, ApplyRequest{Config: cfg, Initiator: by})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := e.Pause(ctx, applied.ID, by); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	now = now.Add(2 * time.Second)
	r, err := e.Resume(ctx, applied.ID, by)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if r.Phase != rollout.PhaseDeploying {
		t.Fatalf("phase after Resume = %q, want deploying", r.Phase)
	}

	// Bake restarts on resume: an immediate Tick must not skip the current step.
	ticked, err := e.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick just after Resume: %v", err)
	}
	if ticked.Phase != rollout.PhaseDeploying || ticked.StepWeight != 10 {
		t.Fatalf("resume must restart the current-step bake: phase=%s weight=%d", ticked.Phase, ticked.StepWeight)
	}

	now = now.Add(2 * time.Second)
	ticked, err = e.Tick(ctx, r.ID, cfg)
	if err != nil {
		t.Fatalf("Tick after bake: %v", err)
	}
	if ticked.Phase != rollout.PhaseDeploying || ticked.StepWeight != 100 {
		t.Fatalf("after resumed bake: phase=%s weight=%d, want deploying/100", ticked.Phase, ticked.StepWeight)
	}
}

func TestAbort_WithoutPriorMarksRolledBack(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }))
	ctx := context.Background()
	by := rollout.Identity{Kind: "human", Name: "felix"}

	applied, err := e.Apply(ctx, ApplyRequest{Config: loadCanaryPause(t), Initiator: by})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	r, err := e.Abort(ctx, applied.ID, by)
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if r.Phase != rollout.PhaseRolledBack {
		t.Fatalf("phase after Abort = %q, want rolled-back", r.Phase)
	}
	if len(fake.applied) != 1 {
		t.Fatalf("abort without prior re-applied: %d applies, want 1", len(fake.applied))
	}
	if !strings.Contains(r.Note, "aborted") {
		t.Errorf("note = %q, want aborted", r.Note)
	}
}

func TestAbort_WithPriorReappliesPrior(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }), WithIDGen(incIDs()))
	ctx := context.Background()
	by := rollout.Identity{Kind: "human", Name: "felix"}

	rolling, err := config.Load([]byte(fakeYAML))
	if err != nil {
		t.Fatalf("load rolling: %v", err)
	}
	first, err := e.Apply(ctx, ApplyRequest{Config: rolling, Initiator: by})
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	prior := fake.applied[len(fake.applied)-1]

	canaryYAML := strings.Replace(canaryPauseYAML, "x: 1", "x: 2", 1)
	canary, err := config.Load([]byte(canaryYAML))
	if err != nil {
		t.Fatalf("load canary: %v", err)
	}
	second, err := e.Apply(ctx, ApplyRequest{Config: canary, Initiator: by})
	if err != nil {
		t.Fatalf("canary Apply: %v", err)
	}
	if second.Phase != rollout.PhaseDeploying {
		t.Fatalf("canary phase = %q, want deploying", second.Phase)
	}

	got, err := e.Abort(ctx, second.ID, by)
	if err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if got.Phase != rollout.PhaseRolledBack {
		t.Fatalf("phase after Abort = %q, want rolled-back", got.Phase)
	}
	last := fake.applied[len(fake.applied)-1]
	if last.Checksum != prior.Checksum {
		t.Errorf("abort applied checksum %q, want prior %q (first rollout %s)", last.Checksum, prior.Checksum, first.ID)
	}
}

func TestPauseResumeAbort_IllegalPhase(t *testing.T) {
	fake := &fakeTarget{}
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	e, _ := newEngine(t, fake, WithClock(func() time.Time { return now }), WithIDGen(incIDs()))
	ctx := context.Background()
	by := rollout.Identity{Kind: "human", Name: "felix"}

	rolling, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t), Initiator: by})
	if err != nil {
		t.Fatalf("rolling Apply: %v", err)
	}
	if rolling.Phase != rollout.PhaseVerifying {
		t.Fatalf("rolling phase = %q, want verifying", rolling.Phase)
	}
	if _, err := e.Pause(ctx, rolling.ID, by); err == nil {
		t.Fatal("Pause of a verifying rollout must fail")
	}

	canary, err := e.Apply(ctx, ApplyRequest{Config: loadCanaryPause(t), Initiator: by})
	if err != nil {
		t.Fatalf("canary Apply: %v", err)
	}
	if _, err := e.Resume(ctx, canary.ID, by); err == nil {
		t.Fatal("Resume of a deploying rollout must fail")
	}

	promoted, err := e.Promote(ctx, rolling.ID, by, false)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if _, err := e.Abort(ctx, promoted.ID, by); err == nil {
		t.Fatal("Abort of a promoted rollout must fail")
	}
}
