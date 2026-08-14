package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/trafficrouting"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const canaryTrafficYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: medium
    spec: {x: 1}
  strategy:
    type: canary
    steps:
      - weight: 20
      - weight: 60
      - weight: 100
  trafficRouting:
    plugin: /unused/in/test
    sha256: deadbeef
    route: app-route
    namespace: prod
    stableService: app-stable
    canaryService: app-canary
`

type recordingRouter struct {
	mu      sync.Mutex
	applied []trafficrouting.Change
}

func (r *recordingRouter) SetWeight(_ context.Context, c trafficrouting.Change) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = append(r.applied, c)
	return nil
}

func TestApply_DrivesTrafficRouterPerStep(t *testing.T) {
	fake := &fakeTarget{}
	rec := &recordingRouter{}
	e, _ := newEngine(t, fake, WithTrafficRouterBuilder(func(context.Context, *config.TrafficRouting) (trafficrouting.Router, error) {
		return rec, nil
	}))
	c, err := config.Load([]byte(canaryTrafficYAML))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Apply(context.Background(), ApplyRequest{Config: c, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var weights []int
	for _, ch := range rec.applied {
		weights = append(weights, ch.Weight)
	}
	if len(weights) != 3 || weights[0] != 20 || weights[1] != 60 || weights[2] != 100 {
		t.Fatalf("per-step canary weights = %v, want [20 60 100]", weights)
	}
	// The route/backends from the spec must reach the router.
	first := rec.applied[0]
	if first.Route != "app-route" || first.StableService != "app-stable" || first.CanaryService != "app-canary" || first.Namespace != "prod" {
		t.Errorf("router change missing spec fields: %+v", first)
	}
	if r.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q", r.Phase)
	}
}

type failingRouter struct{ err error }

func (f failingRouter) SetWeight(context.Context, trafficrouting.Change) error { return f.err }

func TestApply_TrafficFailureAbortsTheStep(t *testing.T) {
	fake := &fakeTarget{}
	e, _ := newEngine(t, fake, WithTrafficRouterBuilder(func(context.Context, *config.TrafficRouting) (trafficrouting.Router, error) {
		return failingRouter{err: errors.New("httproute rejected")}, nil
	}))
	c, err := config.Load([]byte(canaryTrafficYAML))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Apply(context.Background(), ApplyRequest{Config: c, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err == nil || !strings.Contains(err.Error(), "trafficrouter") {
		t.Fatalf("traffic failure must abort apply, got %v", err)
	}
	if r != nil && r.Phase == rollout.PhaseVerifying {
		t.Error("a canary that did not shift traffic must not reach verifying")
	}
}

func TestApply_TrafficFailureAutoRollsBack(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithIDGen(incIDs()), WithTrafficRouterBuilder(func(context.Context, *config.TrafficRouting) (trafficrouting.Router, error) {
		return failingRouter{err: errors.New("gateway 409")}, nil
	}))
	ctx := context.Background()
	prior, err := e.Apply(ctx, ApplyRequest{Config: loadConfig(t), Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("prior apply: %v", err)
	}
	c, err := config.Load([]byte(canaryTrafficYAML + "\n  rollback:\n    auto: true\n"))
	if err != nil {
		t.Fatal(err)
	}
	bad, err := e.Apply(ctx, ApplyRequest{Config: c, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err == nil {
		t.Fatal("traffic failure must error")
	}
	if bad == nil || bad.Phase != rollout.PhaseRolledBack {
		t.Fatalf("phase = %v, want rolled-back", bad)
	}
	last := fake.applied[len(fake.applied)-1]
	if last.Checksum != prior.Desired.Checksum {
		t.Errorf("last applied = %q, want prior %q", last.Checksum, prior.Desired.Checksum)
	}
}
