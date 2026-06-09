package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	itarget "go.klarlabs.de/rolloffs/internal/target"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

const fakeYAML = `
apiVersion: rolloffs.klarlabs.de/v1
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
    type: rolling
`

// fakeTarget is an in-memory Target the registry hands back, so tests can
// inspect what the engine applied and steer Observe/Health.
type fakeTarget struct {
	applied  []pt.Manifest
	fp       pt.Fingerprint
	health   pt.HealthStatus
	applyErr error
}

func (f *fakeTarget) Apply(_ context.Context, m pt.Manifest) (pt.Result, error) {
	if f.applyErr != nil {
		return pt.Result{}, f.applyErr
	}
	f.applied = append(f.applied, m)
	return pt.Result{Changed: true}, nil
}
func (f *fakeTarget) Observe(context.Context) (pt.Fingerprint, error) { return f.fp, nil }
func (f *fakeTarget) Health(context.Context) (pt.HealthStatus, error) { return f.health, nil }
func (f *fakeTarget) Diff(_ context.Context, m pt.Manifest) (string, error) {
	return "diff for " + m.Checksum, nil
}
func (f *fakeTarget) Resources(context.Context) ([]pt.Resource, error) {
	return []pt.Resource{{Kind: "Deployment", Name: "app", Status: "ready 1/1"}}, nil
}

func newEngine(t *testing.T, fake *fakeTarget, extra ...Option) (*Engine, *sqlite.Store) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })

	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	n := 0
	opts := append([]Option{
		WithClock(func() time.Time { return clock }),
		WithIDGen(func() string { n++; return "ro-test" }),
	}, extra...)
	e := New(db, reg, opts...)
	return e, db
}

func loadConfig(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load([]byte(fakeYAML))
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return c
}

func TestApply_DeploysAndPersists(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	ctx := context.Background()

	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t), Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q, want verifying", r.Phase)
	}
	if len(fake.applied) != 1 {
		t.Fatalf("target applied %d times, want 1", len(fake.applied))
	}
	if fake.applied[0].Kind != "fake" || fake.applied[0].Checksum == "" {
		t.Errorf("applied manifest = %+v", fake.applied[0])
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatalf("LoadRollout: %v", err)
	}
	if got.Phase != rollout.PhaseVerifying || got.Initiator.Name != "felix" {
		t.Errorf("persisted rollout = %+v", got)
	}
}

func TestApply_TargetFailure_RecordsRolledBack(t *testing.T) {
	fake := &fakeTarget{applyErr: context.DeadlineExceeded}
	e, db := newEngine(t, fake)
	ctx := context.Background()

	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})
	if err == nil {
		t.Fatal("expected apply error")
	}
	got, _ := db.LoadRollout(ctx, r.ID)
	if got.Phase != rollout.PhaseRolledBack {
		t.Errorf("phase = %q, want rolled-back", got.Phase)
	}
}

func TestApply_AgentRequiresPlan(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)
	_, err := e.Apply(context.Background(), ApplyRequest{
		Config:    loadConfig(t),
		Initiator: rollout.Identity{Kind: "agent", Name: "nomi"},
		Planned:   false,
	})
	if err == nil {
		t.Fatal("agent apply without a plan must be rejected")
	}
	if len(fake.applied) != 0 {
		t.Fatal("target must not be touched when apply is rejected")
	}
}

func TestApply_AgentWithPlan_Deploys(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)
	r, err := e.Apply(context.Background(), ApplyRequest{
		Config:    loadConfig(t),
		Initiator: rollout.Identity{Kind: "agent", Name: "nomi"},
		Planned:   true,
	})
	if err != nil {
		t.Fatalf("agent apply with plan: %v", err)
	}
	if r.Phase != rollout.PhaseVerifying || len(fake.applied) != 1 {
		t.Fatalf("expected deploy; phase=%q applied=%d", r.Phase, len(fake.applied))
	}
}

func TestApply_GatedHaltsAtApproval(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	r, err := e.Apply(context.Background(), ApplyRequest{
		Config:        loadConfig(t),
		NeedsApproval: true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.Phase != rollout.PhaseAwaitingApproval {
		t.Errorf("phase = %q, want awaiting-approval", r.Phase)
	}
	if len(fake.applied) != 0 {
		t.Error("gated rollout must not touch the target")
	}
	got, _ := db.LoadRollout(context.Background(), r.ID)
	if got.Phase != rollout.PhaseAwaitingApproval {
		t.Errorf("persisted phase = %q", got.Phase)
	}
}

func TestObserve_PersistsFingerprint(t *testing.T) {
	fake := &fakeTarget{fp: pt.Fingerprint{Value: "fp-xyz"}}
	e, db := newEngine(t, fake)
	ctx := context.Background()

	c := loadConfig(t)
	fp, err := e.Observe(ctx, c.Spec.Target)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if fp.Value != "fp-xyz" {
		t.Errorf("fp = %q", fp.Value)
	}
	stored, err := db.ObservedState(ctx, c.Spec.Target.Ref)
	if err != nil {
		t.Fatalf("ObservedState: %v", err)
	}
	if stored.Value != "fp-xyz" {
		t.Errorf("stored fp = %q", stored.Value)
	}
}

func TestPlan_DetectsChangeByChecksum(t *testing.T) {
	fake := &fakeTarget{} // fp.Value == "" != checksum -> changed
	e, _ := newEngine(t, fake)
	ctx := context.Background()
	c := loadConfig(t)

	p1, err := e.Plan(ctx, c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !p1.Changed {
		t.Error("expected Changed=true when observed differs from desired")
	}
	// Make the observed fingerprint match the desired checksum -> no change.
	fake.fp = pt.Fingerprint{Value: p1.Desired.Checksum}
	p2, err := e.Plan(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Changed {
		t.Error("expected Changed=false when observed matches desired checksum")
	}
}

func TestVerify_HealthGate(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake)
	ctx := context.Background()
	r, _ := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})

	if _, err := e.Verify(ctx, r.ID); err != nil {
		t.Fatalf("healthy verify should pass: %v", err)
	}
	fake.health = pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "503"}
	if _, err := e.Verify(ctx, r.ID); err == nil {
		t.Fatal("unhealthy verify should fail")
	}
}

func TestPromote(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake)
	ctx := context.Background()
	r, _ := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})

	pr, err := e.Promote(ctx, r.ID)
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if pr.Phase != rollout.PhasePromoted {
		t.Errorf("phase = %q, want promoted", pr.Phase)
	}
	got, _ := db.LoadRollout(ctx, r.ID)
	if got.Phase != rollout.PhasePromoted {
		t.Errorf("persisted phase = %q", got.Phase)
	}
}

func TestRollback_ReappliesPrior(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	ctx := context.Background()
	r, _ := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})

	prior := pt.Manifest{Kind: "fake", Spec: []byte(`{"x":0}`), Checksum: "prior"}
	rb, err := e.Rollback(ctx, r.ID, prior)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rb.Phase != rollout.PhaseRolledBack {
		t.Errorf("phase = %q, want rolled-back", rb.Phase)
	}
	last := fake.applied[len(fake.applied)-1]
	if last.Checksum != "prior" {
		t.Errorf("rollback re-applied %q, want prior manifest", last.Checksum)
	}
	got, _ := db.LoadRollout(ctx, r.ID)
	if got.Desired.Checksum != "prior" {
		t.Errorf("persisted desired = %q, want prior", got.Desired.Checksum)
	}
}

func TestSchedule_AssignsIDAndFires(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	ctx := context.Background()
	due := time.Date(2026, 6, 8, 11, 0, 0, 0, time.UTC) // already past relative to query

	if err := e.Schedule(ctx, rollout.ScheduledRollout{TargetRef: "demo/prod/app", DueAt: due, Desired: pt.Manifest{Kind: "fake"}}); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	got, err := db.DueSchedules(ctx, time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID == "" {
		t.Fatalf("DueSchedules = %+v, want one with assigned id", got)
	}
}

// Compile-time guard: the engine must satisfy whatever the callers expect.
var _ = func() *Engine { return New(nil, nil) }
