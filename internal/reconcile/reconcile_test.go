package reconcile

import (
	"bytes"
	"context"
	"strings"
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

const cfgYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
    spec:
      x: 1
  strategy:
    type: rolling
`

type fakeTarget struct {
	applied []pt.Manifest
	fp      pt.Fingerprint
}

func (f *fakeTarget) Apply(_ context.Context, m pt.Manifest) (pt.Result, error) {
	f.applied = append(f.applied, m)
	f.fp = pt.Fingerprint{Value: m.Checksum} // deploy stamps the checksum
	return pt.Result{Changed: true}, nil
}
func (f *fakeTarget) Observe(context.Context) (pt.Fingerprint, error) { return f.fp, nil }
func (f *fakeTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

func setup(t *testing.T, fake *fakeTarget) (*Reconciler, *bytes.Buffer, *config.Config) {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return clock }), engine.WithIDGen(func() string { return "ro1" }))

	var buf bytes.Buffer
	c, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	return New(eng, audit.New(&buf)), &buf, c
}

var actor = rollout.Identity{Kind: "ci", Name: "reconciler"}

func TestReconcile_DriftDetectedAndReconciled(t *testing.T) {
	fake := &fakeTarget{fp: pt.Fingerprint{Value: "stale"}} // observed != desired
	r, buf, c := setup(t, fake)

	out, err := r.Reconcile(context.Background(), c, actor)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !out.Drift || !out.Reconciled {
		t.Fatalf("expected drift+reconcile, got %+v", out)
	}
	if len(fake.applied) != 1 {
		t.Errorf("drift should trigger one apply, got %d", len(fake.applied))
	}
	if !strings.Contains(buf.String(), `"action":"drift"`) {
		t.Errorf("drift should be audited: %s", buf.String())
	}
}

type failingSmoke struct{}

func (failingSmoke) Run(context.Context, []string) (int, error) { return 1, nil } // exit 1 != expect 0

// TestReconcile_FailedPostDeployRollsBackToPriorNotNew proves Fix B: when the
// post-deploy gate fails, reconcile hands VerifyOrRollback the PRIOR good
// manifest, so the auto-rollback restores it — not the just-applied broken one.
func TestReconcile_FailedPostDeployRollsBackToPriorNotNew(t *testing.T) {
	fake := &fakeTarget{}
	db, err := sqlite.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	n := 0
	eng := engine.New(db, reg,
		engine.WithClock(func() time.Time { return clock }),
		engine.WithIDGen(func() string { n++; return "ro-" + string(rune('a'+n)) }),
		engine.WithSmokeRunner(failingSmoke{}),
	)
	rec := New(eng, nil)
	ctx := context.Background()

	// First reconcile: deploy manifest A (no smoke test → promotes). Prior good.
	good, err := config.Load([]byte(cfgYAML)) // spec x: 1
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Reconcile(ctx, good, actor); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	priorChecksum := fake.fp.Value // A, stamped by Apply

	// Second reconcile: a NEW manifest B whose post-deploy smoke test fails and
	// opts into auto-rollback.
	badYAML := `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
    spec:
      x: 2
  strategy:
    type: rolling
  rollback:
    auto: true
    smokeTest:
      command: ["./smoke.sh"]
      expectExit: 0
`
	bad, err := config.Load([]byte(badYAML))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rec.Reconcile(ctx, bad, actor); err != nil {
		t.Fatalf("failing reconcile: %v", err)
	}
	// The rollback must restore the PRIOR good manifest (A, spec x:1), not the
	// just-applied broken one (B, spec x:2). Before Fix B, reconcile passed
	// rl.Desired (B) as prior, so the "rollback" re-applied the broken version.
	last := fake.applied[len(fake.applied)-1]
	if last.Checksum != priorChecksum {
		t.Fatalf("rolled back to %q, want prior good %q (not the new broken manifest)", last.Checksum, priorChecksum)
	}
	if fake.fp.Value != priorChecksum {
		t.Errorf("live state = %q, want restored prior %q", fake.fp.Value, priorChecksum)
	}
}

func TestReconcile_InSyncNoop(t *testing.T) {
	fake := &fakeTarget{}
	r, _, c := setup(t, fake)
	// First reconcile deploys (create); fake now carries the desired checksum.
	if _, err := r.Reconcile(context.Background(), c, actor); err != nil {
		t.Fatal(err)
	}
	applied := len(fake.applied)

	// Second reconcile: observed == desired → no drift, no further apply.
	out, err := r.Reconcile(context.Background(), c, actor)
	if err != nil {
		t.Fatal(err)
	}
	if out.Drift {
		t.Error("in-sync target should report no drift")
	}
	if len(fake.applied) != applied {
		t.Errorf("in-sync reconcile must not re-apply: %d -> %d", applied, len(fake.applied))
	}
}

func TestReconcile_TicksInFlightCanary(t *testing.T) {
	fake := &fakeTarget{}
	db, err := sqlite.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	eng := engine.New(db, reg,
		engine.WithClock(func() time.Time { return now }),
		engine.WithIDGen(func() string { return "ro-canary" }),
	)
	rec := New(eng, nil)
	yaml := `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
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
	c, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	out, err := rec.Reconcile(ctx, c, actor)
	if err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if out.Rollout == nil || out.Rollout.Phase != rollout.PhaseDeploying {
		t.Fatalf("first Reconcile phase = %v, want deploying", out.Rollout)
	}
	if out.Reconciled {
		t.Fatal("must not verify/promote while the canary is still baking")
	}
	applied := len(fake.applied)

	now = now.Add(50 * time.Millisecond)
	out, err = rec.Reconcile(ctx, c, actor)
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if out.Rollout == nil || out.Rollout.Phase != rollout.PhaseDeploying {
		t.Fatalf("second Reconcile phase = %v, want deploying", out.Rollout)
	}
	if len(fake.applied) != applied {
		t.Fatalf("in-flight tick must not re-apply, got %d", len(fake.applied))
	}

	now = now.Add(50 * time.Millisecond)
	out, err = rec.Reconcile(ctx, c, actor)
	if err != nil {
		t.Fatalf("third Reconcile: %v", err)
	}
	if !out.Reconciled || out.Rollout == nil || out.Rollout.Phase != rollout.PhasePromoted {
		t.Fatalf("third Reconcile = drift=%v reconciled=%v phase=%v, want promoted", out.Drift, out.Reconciled, out.Rollout)
	}
}
