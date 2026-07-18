package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const fakeYAML = `
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
    type: rolling
`

// fakeTarget is an in-memory Target the registry hands back, so tests can
// inspect what the engine applied and steer Observe/Health.
type fakeTarget struct {
	applied  []pt.Manifest
	fp       pt.Fingerprint
	health   pt.HealthStatus
	applyErr error
	diffErr  error // when set, Diff returns this error (drives the drift fail-closed path)
	// referenced + rendered exercise the pt.Renderer capability: when referenced
	// is true the engine stamps the checksum over rendered (default off, so
	// existing tests are unaffected).
	referenced bool
	rendered   []byte
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
	if f.diffErr != nil {
		return "", f.diffErr
	}
	return "diff for " + m.Checksum, nil
}
func (f *fakeTarget) Resources(context.Context) ([]pt.Resource, error) {
	return []pt.Resource{{Kind: "Deployment", Name: "app", Status: "ready 1/1"}}, nil
}
func (f *fakeTarget) Referenced(pt.Manifest) bool { return f.referenced }
func (f *fakeTarget) Render(context.Context, pt.Manifest) ([]byte, error) {
	return f.rendered, nil
}

func newEngine(t *testing.T, fake *fakeTarget, extra ...Option) (*Engine, *sqlite.Store) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
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

func TestApply_PersistsStepProgress(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	ctx := context.Background()

	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t), Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatalf("LoadRollout: %v", err)
	}
	// rolling = 4 steps (25/50/75/100); all passed, so progress is complete.
	if got.StepIndex != 4 || got.StepTotal != 4 || got.StepWeight != 100 {
		t.Errorf("step progress = %d/%d (%d%%), want 4/4 (100%%)", got.StepIndex, got.StepTotal, got.StepWeight)
	}
	recs, err := db.History(ctx, "demo/prod/app")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	var stepNotes int
	for _, rec := range recs {
		if strings.Contains(rec.Note, "passed health gate") {
			stepNotes++
		}
	}
	if stepNotes != 4 {
		t.Errorf("step notes in history = %d, want 4", stepNotes)
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

// TestApply_CapturesAnalysisConfig proves the deploy path persists the
// spec.analysis descriptor on the rollout as opaque JSON, so a later manual
// Verify/Promote can run the same metric-analysis gate as the auto path.
func TestApply_CapturesAnalysisConfig(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake)
	ctx := context.Background()
	c, err := config.Load([]byte(analysisYAML))
	if err != nil {
		t.Fatalf("load analysis config: %v", err)
	}
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatalf("load rollout: %v", err)
	}
	if len(got.Analysis) == 0 {
		t.Fatal("Apply should capture spec.analysis on the rollout, got none")
	}
	var a config.Analysis
	if err := json.Unmarshal(got.Analysis, &a); err != nil {
		t.Fatalf("captured analysis is not valid JSON: %v", err)
	}
	if a.Provider != "prometheus" || a.Condition != "errorRate < 0.05" {
		t.Errorf("captured analysis = %+v, want prometheus provider + errorRate condition", a)
	}
}

// TestApply_NoAnalysisLeavesEmpty proves a config without spec.analysis leaves
// the captured descriptor empty, so the len==0 guard in the manual gate holds.
func TestApply_NoAnalysisLeavesEmpty(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake)
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatalf("load rollout: %v", err)
	}
	if len(got.Analysis) != 0 {
		t.Errorf("no spec.analysis should leave Analysis empty, got %q", got.Analysis)
	}
}

func TestVerify_HealthGate(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake)
	ctx := context.Background()
	r, _ := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})

	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("healthy verify should not error: %v", err)
	}
	if !rep.OK {
		t.Fatalf("healthy verify should pass: %+v", rep.Gates)
	}
	fake.health = pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "503"}
	// A failing gate is a RESULT, not an error: the report says not-OK.
	rep, err = e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("a failing gate must not be an operational error: %v", err)
	}
	if rep.OK {
		t.Fatal("unhealthy verify should report OK=false")
	}
	if !strings.Contains(rep.Reason, "health check failed") {
		t.Errorf("reason = %q, want the health failure", rep.Reason)
	}
	// The dry run must not advance (or otherwise touch) the phase.
	if rep.Rollout.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q, want it untouched at verifying", rep.Rollout.Phase)
	}
}

// TestVerify_RunsMetricAnalysisFails proves a manual Verify runs the same
// metric-analysis gate as the auto path: a healthy target still fails Verify
// when the injected metrics provider breaches the analysis condition.
func TestVerify_RunsMetricAnalysisFails(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	// fixedMetrics(0.2) breaches "errorRate < 0.05".
	e, _ := newEngine(t, fake, WithMetricAnalysis(), WithMetricsProvider(fixedMetrics(0.2)))
	ctx := context.Background()
	c, err := config.Load([]byte(analysisYAML))
	if err != nil {
		t.Fatalf("load analysis config: %v", err)
	}
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("a breaching gate must not be an operational error: %v", err)
	}
	if rep.OK {
		t.Fatal("manual verify should not pass when metric analysis breaches, even with a healthy target")
	}
	if !strings.Contains(rep.Reason, "analysis") {
		t.Errorf("reason = %q, want an analysis failure", rep.Reason)
	}
}

// TestVerify_RunsMetricAnalysisPasses proves a healthy target plus a passing
// analysis clears Verify.
func TestVerify_RunsMetricAnalysisPasses(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithMetricAnalysis(), WithMetricsProvider(fixedMetrics(0.01)))
	ctx := context.Background()
	c, _ := config.Load([]byte(analysisYAML))
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("healthy target + passing analysis should verify: %+v", rep.Gates)
	}
}

// TestVerify_AnalysisNoopWhenDisabled proves the manual analysis gate stays
// opt-in: with analysis disabled (no WithMetricAnalysis) a breaching provider
// is ignored and Verify remains health-only.
func TestVerify_AnalysisNoopWhenDisabled(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake) // analysis off by default
	ctx := context.Background()
	c, _ := config.Load([]byte(analysisYAML))
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("analysis disabled: verify should be health-only: %+v", rep.Gates)
	}
	if g := gateByName(t, rep, GateAnalysis); g.Status != GateSkipped {
		t.Errorf("analysis gate = %+v, want skipped when analysis is disabled", g)
	}
}

// TestVerify_AnalysisNoopWhenAbsent proves that even with analysis enabled and a
// breaching provider, a rollout with no captured analysis config verifies on
// health alone — the len(r.Analysis)==0 guard holds.
func TestVerify_AnalysisNoopWhenAbsent(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithMetricAnalysis(), WithMetricsProvider(fixedMetrics(0.2)))
	ctx := context.Background()
	// fakeYAML carries no spec.analysis, so r.Analysis stays empty.
	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	rep, err := e.Verify(ctx, r.ID)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.OK {
		t.Fatalf("no captured analysis: verify should be health-only: %+v", rep.Gates)
	}
	if g := gateByName(t, rep, GateAnalysis); g.Status != GateSkipped {
		t.Errorf("analysis gate = %+v, want skipped when nothing was captured", g)
	}
}

// TestPromote_RunsMetricAnalysisFails proves a direct manual Promote — which
// can be issued on a freshly-deployed rollout without a prior Verify (the phase
// is already `verifying` after Apply) — cannot skip the metric-analysis gate: a
// breaching provider fails the promote and the rollout stays in verifying.
func TestPromote_RunsMetricAnalysisFails(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake, WithMetricAnalysis(), WithMetricsProvider(fixedMetrics(0.2)))
	ctx := context.Background()
	c, err := config.Load([]byte(analysisYAML))
	if err != nil {
		t.Fatalf("load analysis config: %v", err)
	}
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	_, err = e.Promote(ctx, r.ID)
	if err == nil {
		t.Fatal("direct promote must not bypass a breaching metric-analysis gate")
	}
	if !strings.Contains(err.Error(), "analysis") {
		t.Errorf("promote error = %q, want an analysis failure", err)
	}
	got, err := db.LoadRollout(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q, want it held at verifying (not promoted)", got.Phase)
	}
}

// TestPromote_RunsMetricAnalysisPasses proves the promote gate lets a passing
// analysis through to promoted.
func TestPromote_RunsMetricAnalysisPasses(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithMetricAnalysis(), WithMetricsProvider(fixedMetrics(0.01)))
	ctx := context.Background()
	c, _ := config.Load([]byte(analysisYAML))
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	pr, err := e.Promote(ctx, r.ID)
	if err != nil {
		t.Fatalf("passing analysis should promote: %v", err)
	}
	if pr.Phase != rollout.PhasePromoted {
		t.Errorf("phase = %q, want promoted", pr.Phase)
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
	rb, err := e.Rollback(ctx, r.ID, prior, false)
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

func TestDriftReport_AssertsRolledBackBaseline(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)
	ctx := context.Background()
	r, _ := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)})

	prior := pt.Manifest{Kind: "fake", Spec: []byte(`{"x":0}`), Checksum: "prior"}
	if _, err := e.Rollback(ctx, r.ID, prior, false); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// Live state matches the restored manifest → no drift.
	fake.fp = pt.Fingerprint{Value: "prior"}
	c := loadConfig(t)
	if _, err := e.Observe(ctx, c.Spec.Target); err != nil {
		t.Fatal(err)
	}
	items, err := e.DriftReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Drifted {
		t.Fatalf("matching rolled-back baseline must not drift: %+v", items)
	}

	// Out-of-band change after a rollback → the rolled-back-to manifest is
	// the baseline, so this must be flagged.
	fake.fp = pt.Fingerprint{Value: "tampered"}
	if _, err := e.Observe(ctx, c.Spec.Target); err != nil {
		t.Fatal(err)
	}
	items, err = e.DriftReport(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || !items[0].Drifted {
		t.Fatalf("out-of-band change after rollback must drift: %+v", items)
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
