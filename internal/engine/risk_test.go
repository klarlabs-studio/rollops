package engine

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/rollout"
)

func mustIdentity(kind, name string) rollout.Identity {
	return rollout.Identity{Kind: kind, Name: name}
}

const riskyYAML = `
apiVersion: rolloffs.klarlabs.de/v1
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
	d, err := e.EvaluateRisk(loadRisky(t), RiskInputs{ChangeType: "code", Environment: "prod", BlastRadius: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !d.NeedsApproval {
		t.Errorf("critical/prod/blue-green should need approval; score=%v", d.Score)
	}
}

func TestEvaluateRisk_SensitiveSchema(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	d, _ := e.EvaluateRisk(loadRisky(t), RiskInputs{ChangeType: "schema", Environment: "dev", BlastRadius: 0})
	if !d.Sensitive || !d.NeedsApproval {
		t.Errorf("schema change is sensitive; d=%+v", d)
	}
}

// Risk decision feeds Apply: a gated rollout halts at awaiting-approval.
func TestEvaluateRisk_FeedsApply(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)
	c := loadRisky(t)
	d, _ := e.EvaluateRisk(c, RiskInputs{ChangeType: "schema", Environment: "prod", BlastRadius: 9})

	r, err := e.Apply(context.Background(), ApplyRequest{Config: c, NeedsApproval: d.NeedsApproval})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(r.Phase), "awaiting") {
		t.Errorf("gated rollout phase = %q, want awaiting-approval", r.Phase)
	}
	if len(fake.applied) != 0 {
		t.Error("gated rollout must not touch the target")
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
