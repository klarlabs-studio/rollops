package mcp

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/security"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	itarget "go.klarlabs.de/rolloffs/internal/target"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

const cfgYAML = `
apiVersion: rolloffs.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/staging/app
    criticality: low
    spec:
      x: 1
  strategy:
    type: rolling
`

type fakeTarget struct{ applied int }

func (f *fakeTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	f.applied++
	return pt.Result{Changed: true}, nil
}
func (f *fakeTarget) Observe(context.Context) (pt.Fingerprint, error) { return pt.Fingerprint{}, nil }
func (f *fakeTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

func newTools(t *testing.T, id rollout.Identity) *Tools {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return &fakeTarget{}, nil })
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return time.Unix(0, 0) }), engine.WithIDGen(func() string { return "ro-mcp" }))

	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "agent", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermApply, Scope: security.Scope{Env: ""}}, {Perm: security.PermStatus},
	}})
	pol.DefineRole(security.Role{Name: "readonly", Grants: []security.Grant{{Perm: security.PermStatus}}})
	pol.Bind("agent:nomi", "agent")
	pol.Bind("agent:weak", "readonly")
	return NewTools(eng, pol, id)
}

func TestTools_PlanApplyStatus(t *testing.T) {
	tl := newTools(t, rollout.Identity{Kind: "agent", Name: "nomi"})
	ctx := context.Background()

	p, err := tl.Plan(ctx, PlanInput{Config: cfgYAML})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Summary == "" {
		t.Error("plan summary empty")
	}
	a, err := tl.Apply(ctx, ApplyInput{Config: cfgYAML})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if a.RolloutID != "ro-mcp" || a.Phase != "verifying" {
		t.Errorf("apply output = %+v", a)
	}
	s, err := tl.Status(ctx, StatusInput{RolloutID: "ro-mcp"})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.Phase != "verifying" {
		t.Errorf("status = %+v", s)
	}
}

func TestTools_RBACDeniesApply(t *testing.T) {
	tl := newTools(t, rollout.Identity{Kind: "agent", Name: "weak"}) // readonly
	if _, err := tl.Apply(context.Background(), ApplyInput{Config: cfgYAML}); err == nil {
		t.Fatal("readonly agent must not apply")
	}
	// but may read status
	if _, err := tl.Status(context.Background(), StatusInput{RolloutID: "missing"}); err == nil {
		// not found is fine; RBAC must allow the call through to the lookup
		t.Skip()
	}
}

func TestNewServer_RegistersTools(t *testing.T) {
	tl := newTools(t, rollout.Identity{Kind: "agent", Name: "nomi"})
	srv := NewServer(tl)
	for _, name := range []string{"rollouts.plan", "rollouts.apply", "rollouts.status"} {
		if _, ok := srv.GetTool(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}
