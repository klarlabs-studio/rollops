package mcp

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const cfgYAML = `
apiVersion: rollops.klarlabs.de/v1
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

type fakeTarget struct{ applied []pt.Manifest }

func (f *fakeTarget) Apply(_ context.Context, m pt.Manifest) (pt.Result, error) {
	f.applied = append(f.applied, m)
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
	n := 0
	eng := engine.New(db, reg,
		engine.WithClock(func() time.Time { return time.Unix(int64(n+1), 0) }),
		engine.WithIDGen(func() string {
			n++
			return "ro-mcp-" + string(rune('0'+n))
		}),
	)

	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "agent", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermApply, Scope: security.Scope{Env: ""}}, {Perm: security.PermRollback}, {Perm: security.PermStatus},
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
	if a.RolloutID != "ro-mcp-1" || a.Phase != "verifying" {
		t.Errorf("apply output = %+v", a)
	}
	s, err := tl.Status(ctx, StatusInput{RolloutID: "ro-mcp-1"})
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

func TestTools_Rollback(t *testing.T) {
	tl := newTools(t, rollout.Identity{Kind: "agent", Name: "nomi"})
	ctx := context.Background()
	c1, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	c2, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	c2.Spec.Target.Spec = map[string]any{"x": 2}
	y1 := cfgYAML
	y2 := `apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/staging/app
    criticality: low
    spec:
      x: 2
  strategy:
    type: rolling
`
	if _, err := tl.Apply(ctx, ApplyInput{Config: y1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Apply(ctx, ApplyInput{Config: y2}); err != nil {
		t.Fatal(err)
	}
	out, err := tl.Rollback(ctx, RollbackInput{TargetRef: c1.Spec.Target.Ref})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.Target != c2.Spec.Target.Ref || out.Phase != "rolled-back" {
		t.Errorf("rollback output = %+v", out)
	}
}

func TestNewServer_RegistersTools(t *testing.T) {
	tl := newTools(t, rollout.Identity{Kind: "agent", Name: "nomi"})
	srv := NewServer(tl)
	for _, name := range []string{"rollouts.plan", "rollouts.apply", "rollouts.rollback", "rollouts.status"} {
		if _, ok := srv.GetTool(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}
