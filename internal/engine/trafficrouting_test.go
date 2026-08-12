package engine

import (
	"context"
	"sync"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/trafficrouting"
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
