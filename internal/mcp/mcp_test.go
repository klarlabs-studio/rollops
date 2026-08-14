package mcp

import (
	"context"
	"errors"
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

// newTools builds a Tools bound to a fresh engine and a policy that grants the
// "nomi" agent the full operator role and the "weak" agent read-only. Callers
// pick the identity per request via asAgent(name), mirroring how the transport
// injects a per-caller identity into the handler context.
func newTools(t *testing.T) *Tools {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/m.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
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
		{Perm: security.PermPlan}, {Perm: security.PermApply, Scope: security.Scope{Env: ""}}, {Perm: security.PermRollback}, {Perm: security.PermPromote}, {Perm: security.PermApprove}, {Perm: security.PermStatus},
	}})
	pol.DefineRole(security.Role{Name: "readonly", Grants: []security.Grant{{Perm: security.PermStatus}}})
	pol.Bind("agent:nomi", "agent")
	pol.Bind("agent:weak", "readonly")
	return NewTools(eng, pol)
}

// asAgent returns a context carrying the named agent identity, as the transport
// auth hook would after resolving that caller's bearer token.
func asAgent(name string) context.Context {
	return WithIdentity(context.Background(), rollout.Identity{Kind: "agent", Name: name})
}

func TestTools_PlanApplyStatus(t *testing.T) {
	tl := newTools(t)
	ctx := asAgent("nomi")

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

func TestTools_Promote(t *testing.T) {
	tl := newTools(t)
	ctx := asAgent("nomi")
	if _, err := tl.Apply(ctx, ApplyInput{Config: cfgYAML}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out, err := tl.Promote(ctx, PromoteInput{RolloutID: "ro-mcp-1"})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if out.Phase != "promoted" {
		t.Errorf("promote output = %+v", out)
	}
}

func TestTools_PromoteDeniedForReadonly(t *testing.T) {
	// Per-caller RBAC on one shared engine+policy: nomi (operator) seeds a rollout,
	// then the readonly "weak" caller — same Tools, different context identity —
	// must not promote it.
	tl := newTools(t)
	if _, err := tl.Apply(asAgent("nomi"), ApplyInput{Config: cfgYAML}); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.Promote(asAgent("weak"), PromoteInput{RolloutID: "ro-mcp-1"}); err == nil {
		t.Fatal("readonly agent must not promote")
	}
}

func TestTools_RBACDeniesApply(t *testing.T) {
	tl := newTools(t)
	ctx := asAgent("weak") // readonly
	if _, err := tl.Apply(ctx, ApplyInput{Config: cfgYAML}); err == nil {
		t.Fatal("readonly agent must not apply")
	}
	// but may read status
	if _, err := tl.Status(ctx, StatusInput{RolloutID: "missing"}); err == nil {
		// not found is fine; RBAC must allow the call through to the lookup
		t.Skip()
	}
}

// TestTools_FailClosedWithoutIdentity proves the surface is fail-closed: a call
// whose context carries no authenticated caller is rejected with
// ErrUnauthenticated and never reaches the engine — no fixed fallback identity.
func TestTools_FailClosedWithoutIdentity(t *testing.T) {
	tl := newTools(t)
	ctx := context.Background() // no identity injected

	if _, err := tl.Plan(ctx, PlanInput{Config: cfgYAML}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Plan without identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := tl.Apply(ctx, ApplyInput{Config: cfgYAML}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Apply without identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := tl.Status(ctx, StatusInput{RolloutID: "ro-mcp-1"}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Status without identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := tl.Rollback(ctx, RollbackInput{TargetRef: "demo/staging/app"}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Rollback without identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := tl.Promote(ctx, PromoteInput{RolloutID: "ro-mcp-1"}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Promote without identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := tl.Freeze(ctx, FreezeInput{Active: true}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Freeze without identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := tl.List(ctx, ListInput{}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("List without identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := tl.History(ctx, HistoryInput{TargetRef: "demo/staging/app"}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("History without identity = %v, want ErrUnauthenticated", err)
	}
	if _, err := tl.Drift(ctx, DriftInput{}); !errors.Is(err, ErrUnauthenticated) {
		t.Errorf("Drift without identity = %v, want ErrUnauthenticated", err)
	}
}

func TestTools_Rollback(t *testing.T) {
	tl := newTools(t)
	ctx := asAgent("nomi")
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
	tl := newTools(t)
	srv := NewServer(tl)
	for _, name := range []string{
		"rollouts.plan", "rollouts.apply", "rollouts.rollback", "rollouts.status",
		"rollouts.list", "rollouts.history", "rollouts.drift",
	} {
		if _, ok := srv.GetTool(name); !ok {
			t.Errorf("tool %q not registered", name)
		}
	}
}

func TestTools_ListHistoryDrift(t *testing.T) {
	tl := newTools(t)
	ctx := asAgent("nomi")
	if _, err := tl.Apply(ctx, ApplyInput{Config: cfgYAML}); err != nil {
		t.Fatal(err)
	}
	listed, err := tl.List(ctx, ListInput{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed.Rollouts) != 1 || listed.Rollouts[0].RolloutID != "ro-mcp-1" || listed.Rollouts[0].Target != "demo/staging/app" {
		t.Errorf("list = %+v", listed)
	}
	hist, err := tl.History(ctx, HistoryInput{TargetRef: "demo/staging/app"})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(hist.Records) == 0 {
		t.Fatal("history empty")
	}
	drift, err := tl.Drift(ctx, DriftInput{})
	if err != nil {
		t.Fatalf("Drift: %v", err)
	}
	if len(drift.Items) != 1 || drift.Items[0].Target != "demo/staging/app" {
		t.Errorf("drift = %+v", drift)
	}
}

func TestTools_ListDeniedWithoutStatus(t *testing.T) {
	tl := newTools(t)
	pol := security.NewPolicy()
	pol.DefineRole(security.Role{Name: "planner", Grants: []security.Grant{{Perm: security.PermPlan}}})
	pol.Bind("agent:planner", "planner")
	tl.policy = pol
	if _, err := tl.List(asAgent("planner"), ListInput{}); err == nil {
		t.Fatal("plan-only agent must not list")
	}
}
