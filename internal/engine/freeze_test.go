package engine

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
	pt "go.klarlabs.de/rollops/pkg/target"
)

func TestFreeze_BlocksApplyUntilLifted(t *testing.T) {
	g := &security.Guardrails{Freeze: security.NewFreeze(), Floor: security.DefaultPolicyFloor()}
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithGuardrails(g))
	ctx := context.Background()
	by := rollout.Identity{Kind: "human", Name: "felix"}

	active, reason, err := e.Freeze(ctx, true, by, "incident-42")
	if err != nil || !active || reason != "incident-42" {
		t.Fatalf("engage = active:%v reason:%q err:%v", active, reason, err)
	}
	if a, _ := e.FreezeStatus(); !a {
		t.Error("FreezeStatus should report active")
	}
	if _, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)}); err == nil {
		t.Fatal("apply must be blocked while frozen")
	}

	active, _, err = e.Freeze(ctx, false, by, "")
	if err != nil || active {
		t.Fatalf("lift = active:%v err:%v", active, err)
	}
	if _, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t)}); err != nil {
		t.Fatalf("apply after unfreeze: %v", err)
	}
}

func TestFreeze_SurvivesStoreReopen(t *testing.T) {
	g := &security.Guardrails{Freeze: security.NewFreeze(), Floor: security.DefaultPolicyFloor()}
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, db := newEngine(t, fake, WithGuardrails(g))
	ctx := context.Background()
	by := rollout.Identity{Kind: "human", Name: "felix"}

	if _, _, err := e.Freeze(ctx, true, by, "incident-42"); err != nil {
		t.Fatal(err)
	}

	g2 := &security.Guardrails{Freeze: security.NewFreeze(), Floor: security.DefaultPolicyFloor()}
	rec, err := db.LoadFreeze(ctx)
	if err != nil {
		t.Fatalf("LoadFreeze: %v", err)
	}
	if !rec.Active || rec.Reason != "incident-42" {
		t.Fatalf("persisted freeze = %+v", rec)
	}
	g2.Freeze.Engage(rec.By, rec.Reason)
	e2 := New(db, e.reg, WithGuardrails(g2))
	if _, err := e2.Apply(ctx, ApplyRequest{Config: loadConfig(t)}); err == nil {
		t.Fatal("apply after reopen must be blocked by restored freeze")
	}
}

func TestFreeze_NotConfigured(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{}) // no guardrails wired
	if _, _, err := e.Freeze(context.Background(), true, rollout.Identity{}, ""); err == nil {
		t.Fatal("freeze without guardrails must error")
	}
}
