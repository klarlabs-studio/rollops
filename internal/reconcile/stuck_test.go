package reconcile

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

func setupWithStore(t *testing.T, fake *fakeTarget) (*Reconciler, *sqlite.Store, *config.Config) {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return clock }), engine.WithIDGen(func() string { return "ro-next" }))
	c, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	return New(eng, audit.New(&bytes.Buffer{})), db, c
}

// A rollout whose post-deploy gate failed with auto-rollback off (or
// blocked) is left in verifying. When the live checksum already matches,
// the plan reports no change and nothing ran the gate again, so the row
// stayed verifying for good and every dependsOn dependent was skipped.
func TestReconcile_StuckVerifyingIsVerifiedAgain(t *testing.T) {
	fake := &fakeTarget{}
	r, db, c := setupWithStore(t, fake)
	ctx := context.Background()
	plan, err := r.eng.Plan(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	fake.fp = pt.Fingerprint{Value: plan.Desired.Checksum} // live matches Git
	if err := db.SaveRollout(ctx, rollout.Rollout{
		ID: "ro-stuck", TargetRef: c.Spec.Target.Ref, Phase: rollout.PhaseVerifying,
		Strategy: rollout.StrategyRolling, Desired: plan.Desired,
		CreatedAt: time.Date(2026, 6, 8, 11, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 6, 8, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, c, actor); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	got, _ := db.LoadRollout(ctx, "ro-stuck")
	if got.Phase != rollout.PhasePromoted {
		t.Fatalf("phase = %q; the gate must run again and promote a now-healthy target", got.Phase)
	}
}

// The in-flight check is documented as running before the plan, but Plan
// ran first, so any plan error (an Observe that cannot reach the target)
// also stopped an in-flight rollout from being stepped.
func TestReconcile_InFlightRolloutIsSteppedWhenThePlanFails(t *testing.T) {
	fake := &fakeTarget{observeErr: errors.New("kubernetes: read live checksum: connection refused")}
	r, db, c := setupWithStore(t, fake)
	ctx := context.Background()
	if err := db.SaveRollout(ctx, rollout.Rollout{
		ID: "ro-inflight", TargetRef: c.Spec.Target.Ref, Phase: rollout.PhaseDeploying,
		Strategy:  rollout.StrategyRolling,
		CreatedAt: time.Date(2026, 6, 8, 11, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 6, 8, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	_, _ = r.Reconcile(ctx, c, actor)
	got, _ := db.LoadRollout(ctx, "ro-inflight")
	if got.Phase == rollout.PhaseDeploying {
		t.Fatal("the in-flight rollout was not stepped because the plan failed")
	}
}
