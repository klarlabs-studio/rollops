package engine

import (
	"context"
	"strings"
	"testing"

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
