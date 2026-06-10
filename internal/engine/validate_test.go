package engine

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
)

func TestValidate_ProducesPlanAndOrder(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	v, err := e.Validate(context.Background(), loadConfig(t), nil)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if v.Plan == nil || v.Plan.Action == "" {
		t.Error("validation should carry a plan")
	}
	if len(v.DeployOrder) == 0 {
		t.Error("validation should carry a deploy order")
	}
}

func TestValidate_BlastRadiusFromDeps(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	c := loadConfig(t) // target ref demo/prod/app
	deps := []rollout.Dependency{
		{From: "demo/prod/app", To: "demo/prod/web"},
		{From: "demo/prod/web", To: "demo/prod/cdn"},
	}
	v, err := e.Validate(context.Background(), c, deps)
	if err != nil {
		t.Fatal(err)
	}
	if v.BlastRadius != 2 { // web, cdn transitively depend on app
		t.Errorf("blast radius = %d, want 2", v.BlastRadius)
	}
}

func TestValidate_RejectsCycle(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	c := loadConfig(t)
	deps := []rollout.Dependency{
		{From: "demo/prod/app", To: "x"},
		{From: "x", To: "demo/prod/app"},
	}
	if _, err := e.Validate(context.Background(), c, deps); err == nil {
		t.Fatal("expected cycle rejection")
	}
}
