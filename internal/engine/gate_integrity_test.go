package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// Fix D: a post-deploy health gate that cannot even build its target must fail
// CLOSED (treat as a failed gate), never fall through to promote.
func TestRunPostDeployChecks_BuildErrorFailsClosed(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	c := loadAutoRollback(t) // rollback.auto true → the health gate runs
	// A manifest whose Kind is not registered makes buildTarget fail.
	r := rollout.Rollout{TargetRef: "demo/prod/app", Desired: pt.Manifest{Kind: "not-a-registered-kind"}}

	failed, reason, _ := e.runPostDeployChecks(context.Background(), r, c)
	if !failed {
		t.Fatal("a health gate that cannot build its target must fail closed")
	}
	if !strings.Contains(reason, "health gate unavailable") {
		t.Errorf("reason = %q, want 'health gate unavailable: ...'", reason)
	}
}

const fullVerifyYAML = `
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
  verification: full
`

// Fix E: under `verification: full` with a matching checksum, a diff ERROR must
// be treated as drift (fail closed) so reconcile re-applies, not silently
// reported "in sync".
func TestPlan_FullVerification_DiffErrorIsDrift(t *testing.T) {
	fake := &fakeTarget{diffErr: errors.New("kubectl diff: connection refused")}
	e, _ := newEngine(t, fake)
	c, err := config.Load([]byte(fullVerifyYAML))
	if err != nil {
		t.Fatal(err)
	}
	// Make the shallow stamp match, so the deep diff runs.
	m, _ := manifestFromConfig(c, "")
	fake.fp = pt.Fingerprint{Value: m.Checksum}

	p, err := e.Plan(context.Background(), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !p.DeepDrift || !p.Changed {
		t.Fatalf("full-verification diff error must be treated as drift: DeepDrift=%v Changed=%v", p.DeepDrift, p.Changed)
	}
}

// Companion: in detect mode a diff error stays alert-only (unchanged behaviour),
// never asserting drift.
func TestPlan_DetectVerification_DiffErrorNotDrift(t *testing.T) {
	fake := &fakeTarget{diffErr: errors.New("diff failed")}
	e, _ := newEngine(t, fake)
	c := loadConfig(t) // no verification set → defaults to detect
	m, _ := manifestFromConfig(c, "")
	fake.fp = pt.Fingerprint{Value: m.Checksum}

	p, err := e.Plan(context.Background(), c)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.DeepDrift || p.Changed {
		t.Errorf("detect-mode diff error must not assert drift: DeepDrift=%v Changed=%v", p.DeepDrift, p.Changed)
	}
}

// Fix F: an engaged emergency freeze must block a scheduled apply at fire time —
// the queue is not a freeze bypass.
func TestFireDueSchedules_FreezeBlocks(t *testing.T) {
	g := &security.Guardrails{Freeze: security.NewFreeze(), Floor: security.DefaultPolicyFloor()}
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithGuardrails(g))
	ctx := context.Background()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

	if err := e.Schedule(ctx, rollout.ScheduledRollout{
		ID: "s-past", TargetRef: "demo/prod/app", DueAt: now.Add(-time.Hour),
		Desired: pt.Manifest{Kind: "fake", Checksum: "due"}, Initiator: rollout.Identity{Kind: "human", Name: "felix"},
	}); err != nil {
		t.Fatal(err)
	}
	g.Freeze.Engage(rollout.Identity{Kind: "human", Name: "sre"}, "incident-9")

	fired, err := e.FireDueSchedules(ctx, now)
	if err == nil {
		t.Fatal("a frozen scheduled apply must return an error")
	}
	if len(fired) != 0 {
		t.Errorf("no schedule should fire while frozen, got %d", len(fired))
	}
	if len(fake.applied) != 0 {
		t.Errorf("frozen scheduled apply must not touch the target, got %d applies", len(fake.applied))
	}
}

// Fix F: a scheduled apply cannot self-approve past a policy floor that demands
// human approval (here a critical-criticality target).
func TestFireDueSchedules_PolicyFloorBlocks(t *testing.T) {
	g := &security.Guardrails{
		Freeze: security.NewFreeze(),
		Floor:  security.PolicyFloor{CriticalTargets: map[string]struct{}{"demo/prod/app": {}}},
	}
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithGuardrails(g))
	ctx := context.Background()
	now := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

	if err := e.Schedule(ctx, rollout.ScheduledRollout{
		ID: "s-crit", TargetRef: "demo/prod/app", DueAt: now.Add(-time.Hour),
		Desired: pt.Manifest{Kind: "fake", Checksum: "due"}, Initiator: rollout.Identity{Kind: "human", Name: "felix"},
	}); err != nil {
		t.Fatal(err)
	}
	fired, err := e.FireDueSchedules(ctx, now)
	if err == nil {
		t.Fatal("a scheduled apply requiring approval must not self-approve")
	}
	if len(fired) != 0 || len(fake.applied) != 0 {
		t.Errorf("floor-gated scheduled apply must not deploy: fired=%d applied=%d", len(fired), len(fake.applied))
	}
}

// Fix G: a freeze engaged after a rollout was gated must block the approve-driven
// deploy too — approval is not a freeze bypass.
func TestApprove_FreezeBlocks(t *testing.T) {
	g := &security.Guardrails{Freeze: security.NewFreeze(), Floor: security.DefaultPolicyFloor()}
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithGuardrails(g))
	ctx := context.Background()

	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t), NeedsApproval: true, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	g.Freeze.Engage(rollout.Identity{Kind: "human", Name: "sre"}, "incident-9")

	if _, err := e.Approve(ctx, r.ID, rollout.Identity{Kind: "human", Name: "mallory"}); err == nil {
		t.Fatal("approve must be blocked while frozen")
	}
	if len(fake.applied) != 0 {
		t.Errorf("frozen approve must not touch the target, got %d applies", len(fake.applied))
	}
}

// Fix H: the approver must differ from the initiator (four-eyes), enforced by
// default.
func TestApprove_SelfApproveBlocked(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake)
	ctx := context.Background()
	felix := rollout.Identity{Kind: "human", Name: "felix"}

	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t), NeedsApproval: true, Initiator: felix})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := e.Approve(ctx, r.ID, felix); err == nil || !strings.Contains(err.Error(), "four-eyes") {
		t.Fatalf("self-approve must be rejected, err = %v", err)
	}
	if len(fake.applied) != 0 {
		t.Errorf("rejected self-approve must not deploy, got %d applies", len(fake.applied))
	}
	// A different approver succeeds.
	if _, err := e.Approve(ctx, r.ID, rollout.Identity{Kind: "human", Name: "morgan"}); err != nil {
		t.Fatalf("distinct approver must be allowed: %v", err)
	}
}

// Fix H: the ROLLOPS_ALLOW_SELF_APPROVE opt-out permits self-approval for
// single-operator setups.
func TestApprove_SelfApproveOptOut(t *testing.T) {
	orig := allowSelfApprove
	allowSelfApprove = func() bool { return true }
	defer func() { allowSelfApprove = orig }()

	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake)
	ctx := context.Background()
	felix := rollout.Identity{Kind: "human", Name: "felix"}

	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t), NeedsApproval: true, Initiator: felix})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := e.Approve(ctx, r.ID, felix)
	if err != nil {
		t.Fatalf("opt-out self-approve must be allowed: %v", err)
	}
	if out.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q, want verifying", out.Phase)
	}
}

// Fix H: system/reconcile initiators are exempt from four-eyes — they are not a
// human approver to collude with.
func TestApprove_SystemInitiatorExemptFromFourEyes(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake)
	ctx := context.Background()

	// A gated rollout with no real initiator (system/reconcile).
	r, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t), NeedsApproval: true, Initiator: rollout.Identity{Kind: "ci", Name: "system"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := e.Approve(ctx, r.ID, rollout.Identity{Kind: "ci", Name: "system"}); err != nil {
		t.Fatalf("system initiator must be exempt from four-eyes: %v", err)
	}
}
