package engine

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/risk"
	"go.klarlabs.de/rollops/internal/rollout"
	policyv1 "go.klarlabs.de/rollops/pkg/policy/v1"
)

// gated reports what the risk gate's decision means for a rollout: a condition
// the operation carries that nobody has satisfied yet. Reading d.Allowed alone
// would say the opposite, which is why call sites go through Permits.
func gated(d policyv1.PolicyDecision) bool {
	return policyv1.Permits(d, nil) != nil
}

func mustIdentity(kind, name string) rollout.Identity {
	return rollout.Identity{Kind: kind, Name: name}
}

const riskyYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: payments
spec:
  target:
    kind: fake
    ref: payments/prod/api
    criticality: critical
    spec:
      x: 1
  strategy:
    type: blue-green
  risk:
    threshold: 0.5
    sensitive: 'changeType == "schema"'
`

func loadRisky(t *testing.T) *config.Config {
	t.Helper()
	c, err := config.Load([]byte(riskyYAML))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

func TestEvaluateRisk_HighRiskNeedsApproval(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	d, _, err := e.EvaluateRisk(context.Background(), loadRisky(t), RiskInputs{ChangeType: "code", Environment: "prod", BlastRadius: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !gated(d) {
		t.Errorf("critical/prod/blue-green should need approval; score=%v", d.Risk.Score)
	}
}

func TestEvaluateRisk_SensitiveSchema(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	d, _, _ := e.EvaluateRisk(context.Background(), loadRisky(t), RiskInputs{ChangeType: "schema", Environment: "dev", BlastRadius: 0})
	if !risk.Sensitive(d) || !gated(d) {
		t.Errorf("schema change is sensitive; d=%+v", d)
	}
}

func TestEvaluateRisk_HistoricalRollbackRaisesScore(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	ctx := context.Background()
	c := loadRisky(t)
	c.Spec.Target.Criticality = "low"
	c.Spec.Strategy.Type = "canary"
	c.Spec.Risk.Threshold = 0.12
	c.Spec.Risk.Sensitive = ""
	c.Spec.Risk.History = config.RiskHistory{Lookback: 5, Weight: 0.2, MaxFailures: 1}

	before, beforeFailures, err := e.EvaluateRisk(ctx, c, RiskInputs{ChangeType: "config", Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if beforeFailures != 0 {
		t.Fatalf("recent failures = %d before any rollback was seeded", beforeFailures)
	}
	if gated(before) {
		t.Fatalf("no history should auto-proceed: %+v", before)
	}

	if err := db.SaveRollout(ctx, rollout.Rollout{
		ID:        "prior-failure",
		TargetRef: c.Spec.Target.Ref,
		Phase:     rollout.PhaseRolledBack,
	}); err != nil {
		t.Fatalf("seed history: %v", err)
	}

	after, afterFailures, err := e.EvaluateRisk(ctx, c, RiskInputs{ChangeType: "config", Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if afterFailures != 1 {
		t.Errorf("recent failures = %d, want the seeded rollback counted", afterFailures)
	}
	if !gated(after) || after.Risk.Score <= before.Risk.Score {
		t.Fatalf("rollback history should raise risk above threshold: before=%+v after=%+v", before, after)
	}
}

// Risk decision feeds Apply: a gated rollout halts at awaiting-approval.
func TestEvaluateRisk_FeedsApply(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	c := loadRisky(t)
	d, _, _ := e.EvaluateRisk(context.Background(), c, RiskInputs{ChangeType: "schema", Environment: "prod", BlastRadius: 9})

	r, err := e.Apply(context.Background(), ApplyRequest{Config: c, NeedsApproval: gated(d), Risk: RiskInputs{ChangeType: "schema", Environment: "prod", BlastRadius: 9}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.Phase), "awaiting") {
		t.Errorf("gated rollout phase = %q, want awaiting-approval", r.Phase)
	}
	if len(fake.applied) != 0 {
		t.Error("gated rollout must not touch the target")
	}
	if r.RiskScore <= 0 {
		t.Errorf("gated apply must persist a blast-radius score, got %v", r.RiskScore)
	}
	got, err := db.LoadRollout(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RiskScore != r.RiskScore {
		t.Errorf("stored RiskScore = %v, want %v", got.RiskScore, r.RiskScore)
	}
}

func TestApprove_DeploysGatedRollout(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	c := loadRisky(t)
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c, NeedsApproval: true})

	out, err := e.Approve(context.Background(), r.ID, mustIdentity("human", "felix"))
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if string(out.Phase) != "verifying" {
		t.Errorf("approved phase = %q, want verifying", out.Phase)
	}
	if len(fake.applied) != 1 {
		t.Errorf("approve should deploy; applied=%d", len(fake.applied))
	}
	got, _ := db.LoadRollout(context.Background(), r.ID)
	if string(got.Phase) != "verifying" {
		t.Errorf("persisted phase = %q", got.Phase)
	}
}

func TestReject_RollsBackWithoutDeploy(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)
	c := loadRisky(t)
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: c, NeedsApproval: true})

	out, err := e.Reject(context.Background(), r.ID, mustIdentity("human", "felix"))
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if string(out.Phase) != "rolled-back" {
		t.Errorf("rejected phase = %q, want rolled-back", out.Phase)
	}
	if len(fake.applied) != 0 {
		t.Error("reject must not deploy")
	}
}

func TestRiskFromConfig_FillsEnvAndDefaultsChangeToConfig(t *testing.T) {
	c := loadRisky(t)
	c.Spec.Target.Env = "prod"
	in := RiskFromConfig(c)
	if in.Environment != "prod" || in.ChangeType != "config" || in.BlastRadius != 0 {
		t.Errorf("got %+v", in)
	}
}

func TestRiskFromConfig_MigrateIsSchema(t *testing.T) {
	c := loadRisky(t)
	c.Spec.Database = &config.Database{Migrate: &config.DatabaseRollback{Command: []string{"migrate", "up"}}}
	in := RiskFromConfig(c)
	if in.ChangeType != "schema" {
		t.Errorf("change = %q, want schema", in.ChangeType)
	}
}

func TestRiskFromConfig_BlastRadiusFromDeps(t *testing.T) {
	c := loadRisky(t)
	in := RiskFromConfig(c, rollout.Dependency{From: c.Spec.Target.Ref, To: "payments/prod/worker"})
	if in.BlastRadius != 1 {
		t.Errorf("blast = %d, want 1", in.BlastRadius)
	}
}

func TestApply_UsesRiskFromConfigSignals(t *testing.T) {
	fake := &fakeTarget{}
	e, db := newEngine(t, fake)
	c := loadRisky(t)
	c.Spec.Target.Env = "prod"
	r, err := e.Apply(context.Background(), ApplyRequest{Config: c, Risk: RiskFromConfig(c)})
	if err != nil {
		t.Fatal(err)
	}
	if r.RiskScore <= 0 {
		t.Errorf("prod+critical apply must store a score, got %v", r.RiskScore)
	}
	got, err := db.LoadRollout(context.Background(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.RiskScore != r.RiskScore {
		t.Errorf("stored %v want %v", got.RiskScore, r.RiskScore)
	}
}
