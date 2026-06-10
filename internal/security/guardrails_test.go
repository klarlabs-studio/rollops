package security

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
)

func TestPolicyFloor_AlwaysApproveConditions(t *testing.T) {
	f := DefaultPolicyFloor()
	f.CriticalTargets["payments/prod/ledger"] = struct{}{}

	cases := []struct {
		name string
		in   FloorInput
		want bool
	}{
		{"critical criticality", FloorInput{Criticality: "critical"}, true},
		{"listed critical target", FloorInput{TargetRef: "payments/prod/ledger"}, true},
		{"prod schema", FloorInput{Environment: "prod", ChangeType: "schema"}, true},
		{"dev schema", FloorInput{Environment: "dev", ChangeType: "schema"}, false},
		{"prod config", FloorInput{Environment: "prod", ChangeType: "config"}, false},
	}
	for _, c := range cases {
		if got := f.MustApprove(c.in); got != c.want {
			t.Errorf("%s: MustApprove = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFreeze_BlocksApply(t *testing.T) {
	g := &Guardrails{Freeze: NewFreeze(), Floor: DefaultPolicyFloor()}
	if _, err := g.CheckApply(context.Background(), rollout.Identity{Kind: "human", Name: "felix"}, FloorInput{}); err != nil {
		t.Fatalf("unfrozen should pass: %v", err)
	}
	g.Freeze.Engage(rollout.Identity{Kind: "human", Name: "felix"}, "incident")
	if _, err := g.CheckApply(context.Background(), rollout.Identity{Kind: "human", Name: "felix"}, FloorInput{}); err != ErrFrozen {
		t.Fatalf("frozen should block, got %v", err)
	}
	g.Freeze.Lift(rollout.Identity{Kind: "human", Name: "felix"})
	if active, _ := g.Freeze.Active(); active {
		t.Error("freeze should be lifted")
	}
}

func TestAgentRateLimit(t *testing.T) {
	g := &Guardrails{
		Floor:      DefaultPolicyFloor(),
		AgentLimit: NewAgentLimiter(2, time.Minute),
	}
	ctx := context.Background()
	nomi := rollout.Identity{Kind: "agent", Name: "nomi"}

	if _, err := g.CheckApply(ctx, nomi, FloorInput{}); err != nil {
		t.Fatalf("1st agent apply: %v", err)
	}
	if _, err := g.CheckApply(ctx, nomi, FloorInput{}); err != nil {
		t.Fatalf("2nd agent apply: %v", err)
	}
	if _, err := g.CheckApply(ctx, nomi, FloorInput{}); err != ErrRateLimited {
		t.Fatalf("3rd agent apply should be rate limited, got %v", err)
	}
	// Humans are not rate-limited.
	human := rollout.Identity{Kind: "human", Name: "felix"}
	for i := 0; i < 5; i++ {
		if _, err := g.CheckApply(ctx, human, FloorInput{}); err != nil {
			t.Fatalf("human apply %d should never be limited: %v", i, err)
		}
	}
}

func TestCheckApply_FloorForcesApproval(t *testing.T) {
	g := &Guardrails{Floor: DefaultPolicyFloor()}
	force, err := g.CheckApply(context.Background(), rollout.Identity{Kind: "agent", Name: "nomi"},
		FloorInput{Environment: "prod", ChangeType: "schema"})
	if err != nil {
		t.Fatal(err)
	}
	if !force {
		t.Error("prod schema must force approval via the non-bypassable floor")
	}
}
