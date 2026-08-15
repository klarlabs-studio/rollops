package engine

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
)

func TestPlan_ActionCreate(t *testing.T) {
	fake := &fakeTarget{} // no observed state
	e, _ := newEngine(t, fake)
	p, err := e.Plan(context.Background(), loadConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if p.Action != PlanCreate || !p.Changed {
		t.Fatalf("action = %q changed=%v, want create/true", p.Action, p.Changed)
	}
	if !strings.Contains(p.Summary, "create") {
		t.Errorf("summary = %q", p.Summary)
	}
}

func TestPlan_ActionNoop(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)
	first, _ := e.Plan(context.Background(), loadConfig(t))
	fake.fp = pt.Fingerprint{Value: first.Desired.Checksum} // observed == desired
	p, _ := e.Plan(context.Background(), loadConfig(t))
	if p.Action != PlanNoop || p.Changed {
		t.Fatalf("action = %q changed=%v, want noop/false", p.Action, p.Changed)
	}
}

func TestNewPlan_DeepDriftUpgradesNoopToUpdate(t *testing.T) {
	desired := pt.Manifest{Kind: "kubernetes", Checksum: "abc123"}
	cur := pt.Fingerprint{Value: "abc123"} // stamp matches desired → shallow says noop

	if p := newPlan("t/p/a", desired, cur, false); p.Action != PlanNoop || p.Changed {
		t.Fatalf("shallow: action=%q changed=%v, want noop/false", p.Action, p.Changed)
	}
	p := newPlan("t/p/a", desired, cur, true)
	if p.Action != PlanUpdate || !p.Changed || !p.DeepDrift {
		t.Fatalf("full: action=%q changed=%v deepDrift=%v, want update/true/true", p.Action, p.Changed, p.DeepDrift)
	}
	if !strings.Contains(p.Summary, "drifted") || !strings.Contains(p.Summary, "full verification") {
		t.Errorf("summary = %q, want deep-drift message", p.Summary)
	}
}

func TestPlan_ActionUpdate(t *testing.T) {
	fake := &fakeTarget{fp: pt.Fingerprint{Value: "deadbeefcafe0000"}} // different from desired
	e, _ := newEngine(t, fake)
	p, _ := e.Plan(context.Background(), loadConfig(t))
	if p.Action != PlanUpdate || !p.Changed {
		t.Fatalf("action = %q changed=%v, want update/true", p.Action, p.Changed)
	}
	if !strings.Contains(p.Summary, "update") || !strings.Contains(p.Summary, "→") {
		t.Errorf("summary = %q", p.Summary)
	}
}

func TestPlan_HonestStrategyNames(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake)
	ctx := context.Background()

	canary, err := config.Load([]byte(`
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
    spec: {x: 1}
  strategy:
    type: canary
    steps:
      - weight: 10
        pause: 1s
      - weight: 100
`))
	if err != nil {
		t.Fatalf("load canary: %v", err)
	}
	p, err := e.Plan(ctx, canary)
	if err != nil {
		t.Fatalf("Plan canary: %v", err)
	}
	if !strings.Contains(p.Summary, "health-gated bake") {
		t.Errorf("canary plan = %q, want health-gated bake", p.Summary)
	}

	bg, err := config.Load([]byte(`
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
    spec: {x: 1}
  strategy:
    type: blue-green
`))
	if err != nil {
		t.Fatalf("load blue-green: %v", err)
	}
	p, err = e.Plan(ctx, bg)
	if err != nil {
		t.Fatalf("Plan blue-green: %v", err)
	}
	if !strings.Contains(p.Summary, "full cutover") {
		t.Errorf("blue-green plan = %q, want full cutover", p.Summary)
	}

	rolling := loadConfig(t)
	p, err = e.Plan(ctx, rolling)
	if err != nil {
		t.Fatalf("Plan rolling: %v", err)
	}
	if strings.Contains(p.Summary, "health-gated bake") || strings.Contains(p.Summary, "full cutover") {
		t.Errorf("rolling plan must not over-explain: %q", p.Summary)
	}
}
