package engine

import (
	"context"
	"sync"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/featureflags"
	"go.klarlabs.de/rollops/internal/rollout"
)

const canaryFlagYAML = `
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
      - weight: 25
      - weight: 100
  featureFlags:
    plugin: /unused/in/test
    sha256: deadbeef
    flag: checkout
    environment: prod
    when: both
`

type recordingFlagProvider struct {
	mu      sync.Mutex
	applied []featureflags.Change
}

func (r *recordingFlagProvider) ApplyFlag(_ context.Context, c featureflags.Change) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applied = append(r.applied, c)
	return nil
}

func TestApply_DrivesFeatureFlagPerStep(t *testing.T) {
	fake := &fakeTarget{}
	rec := &recordingFlagProvider{}
	e, _ := newEngine(t, fake, WithFlagProviderBuilder(func(context.Context, *config.FeatureFlags) (featureflags.Provider, error) {
		return rec, nil
	}))
	c, err := config.Load([]byte(canaryFlagYAML))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Apply(context.Background(), ApplyRequest{Config: c, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// canary 25 → 100 → both steps drive the flag to matching percentages.
	got := percentages(rec)
	if len(got) != 2 || got[0] != 25 || got[1] != 100 {
		t.Fatalf("per-step flag percentages = %v, want [25 100]", got)
	}
	if r.Phase != rollout.PhaseVerifying {
		t.Errorf("phase = %q", r.Phase)
	}
}

func percentages(r *recordingFlagProvider) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, 0, len(r.applied))
	for _, c := range r.applied {
		out = append(out, c.Percentage)
	}
	return out
}
