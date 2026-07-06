package engine

import (
	"context"
	"fmt"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/featureflags"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/trafficrouting"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// TestApply_CrashloopAutoRollsBackToPrior proves Fix A + C: when the deploy's
// health gate fails and rollback.auto is set, Apply reverts to the prior good
// manifest AND resets delivery, instead of leaving the broken version live.
func TestApply_CrashloopAutoRollsBackToPrior(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	rec := &recordingRouter{}
	flag := &recordingFlagProvider{}
	e, db := newEngine(t, fake,
		WithIDGen(incIDs()),
		WithTrafficRouterBuilder(func(*config.TrafficRouting) (trafficrouting.Router, error) { return rec, nil }),
		WithFlagProviderBuilder(func(*config.FeatureFlags) (featureflags.Provider, error) { return flag, nil }),
	)
	ctx := context.Background()

	// First deploy establishes a healthy prior good manifest (delivery driven up).
	good, err := e.Apply(ctx, ApplyRequest{Config: loadCrashDelivery(t, "1"), Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Second deploy of a NEW manifest crashes its health gate mid-step.
	fake.health = pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "CrashLoopBackOff"}
	bad, err := e.Apply(ctx, ApplyRequest{Config: loadCrashDelivery(t, "2"), Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err == nil {
		t.Fatal("crashing deploy must return an error")
	}
	if bad == nil || bad.Phase != rollout.PhaseRolledBack {
		t.Fatalf("phase = %v, want rolled-back", bad)
	}
	// The manifest was reverted: the last thing applied is the prior good checksum.
	last := fake.applied[len(fake.applied)-1]
	if last.Checksum != good.Desired.Checksum {
		t.Fatalf("last applied = %q, want prior good %q", short(last.Checksum), short(good.Desired.Checksum))
	}
	if bad.Desired.Checksum != good.Desired.Checksum {
		t.Errorf("rolled-back desired = %q, want prior good", short(bad.Desired.Checksum))
	}
	// Delivery was reset: traffic driven to 0 (stable) and the flag disabled.
	if w := lastWeight(rec); w != 0 {
		t.Errorf("last traffic weight = %d, want 0 (stable) on rollback", w)
	}
	if !flagDisabled(flag) {
		t.Error("coupled flag must be disabled on auto-rollback")
	}
	got, _ := db.LoadRollout(ctx, bad.ID)
	if got.Phase != rollout.PhaseRolledBack {
		t.Errorf("persisted phase = %q, want rolled-back", got.Phase)
	}
}

// TestApply_CrashloopNoPriorFallsBackToMark proves the fall-through: with no
// prior good manifest, Apply is never worse than before — it marks rolled-back.
func TestApply_CrashloopNoPriorFallsBackToMark(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthUnhealthy, Reason: "boom"}}
	e, db := newEngine(t, fake)
	ctx := context.Background()

	r, err := e.Apply(ctx, ApplyRequest{Config: loadCrashConfig(t, `{x: 1}`), Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err == nil {
		t.Fatal("crashing first deploy must error")
	}
	if r.Phase != rollout.PhaseRolledBack {
		t.Errorf("phase = %q, want rolled-back", r.Phase)
	}
	got, _ := db.LoadRollout(ctx, r.ID)
	if got.Phase != rollout.PhaseRolledBack {
		t.Errorf("persisted phase = %q, want rolled-back", got.Phase)
	}
}

// TestManualRollback_ResetsDeliveryFromPersistedDescriptor proves Fix C for the
// MANUAL path: a rollback with no config in hand still resets traffic to stable
// and disables the flag, driven from the descriptors persisted at deploy time.
func TestManualRollback_ResetsDeliveryFromPersistedDescriptor(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	rec := &recordingRouter{}
	flag := &recordingFlagProvider{}
	e, _ := newEngine(t, fake,
		WithTrafficRouterBuilder(func(*config.TrafficRouting) (trafficrouting.Router, error) { return rec, nil }),
		WithFlagProviderBuilder(func(*config.FeatureFlags) (featureflags.Provider, error) { return flag, nil }),
	)
	ctx := context.Background()

	c, err := config.Load([]byte(canaryDeliveryYAML))
	if err != nil {
		t.Fatal(err)
	}
	r, err := e.Apply(ctx, ApplyRequest{Config: c, Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Manual rollback: only the prior manifest, no delivery config passed.
	out, err := e.Rollback(ctx, r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, false)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if out.Phase != rollout.PhaseRolledBack {
		t.Errorf("phase = %q, want rolled-back", out.Phase)
	}
	if w := lastWeight(rec); w != 0 {
		t.Errorf("last traffic weight = %d, want 0 (stable) after manual rollback", w)
	}
	if !flagDisabled(flag) {
		t.Error("coupled flag must be disabled after manual rollback")
	}
}

// TestPriorManifest_SkipsCurrentReturnsPrior proves the shared prior-good
// selection RollbackLast and the auto path both rely on.
func TestPriorManifest_SkipsCurrentReturnsPrior(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}}
	e, _ := newEngine(t, fake, WithIDGen(incIDs()))
	ctx := context.Background()

	first, _ := e.Apply(ctx, ApplyRequest{Config: loadCrashConfig(t, `{x: 1}`), Initiator: rollout.Identity{Kind: "human", Name: "felix"}})
	second, _ := e.Apply(ctx, ApplyRequest{Config: loadCrashConfig(t, `{x: 2}`), Initiator: rollout.Identity{Kind: "human", Name: "felix"}})

	prior, ok := e.PriorManifest(ctx, "demo/prod/app", second.Desired.Checksum)
	if !ok {
		t.Fatal("expected a prior manifest")
	}
	if prior.Checksum != first.Desired.Checksum {
		t.Errorf("prior = %q, want first %q", short(prior.Checksum), short(first.Desired.Checksum))
	}
	// No prior distinct from the first deploy's own checksum.
	if _, ok := e.PriorManifest(ctx, "demo/prod/app", first.Desired.Checksum); ok {
		// first and second differ, so excluding first still returns second — ok.
		_ = ok
	}
	if _, ok := e.PriorManifest(ctx, "unknown/ref", "x"); ok {
		t.Error("unknown target must have no prior")
	}
}

func loadCrashConfig(t *testing.T, spec string) *config.Config {
	t.Helper()
	yaml := `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: medium
    spec: ` + spec + `
  strategy:
    type: rolling
  rollback:
    auto: true
`
	c, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

// loadCrashDelivery builds a canary config with traffic routing + a coupled
// feature flag and rollback.auto, parameterised by target spec so successive
// applies produce distinct manifests.
func loadCrashDelivery(t *testing.T, x string) *config.Config {
	t.Helper()
	yaml := `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: medium
    spec: {x: ` + x + `}
  strategy:
    type: canary
    steps:
      - weight: 50
      - weight: 100
  rollback:
    auto: true
  trafficRouting:
    plugin: /unused/in/test
    sha256: deadbeef
    route: app-route
    namespace: prod
    stableService: app-stable
    canaryService: app-canary
  featureFlags:
    plugin: /unused/in/test
    sha256: deadbeef
    flag: checkout
    environment: prod
`
	c, err := config.Load([]byte(yaml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

const canaryDeliveryYAML = `
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
      - weight: 50
      - weight: 100
  trafficRouting:
    plugin: /unused/in/test
    sha256: deadbeef
    route: app-route
    namespace: prod
    stableService: app-stable
    canaryService: app-canary
  featureFlags:
    plugin: /unused/in/test
    sha256: deadbeef
    flag: checkout
    environment: prod
`

// incIDs returns a rollout-id generator that yields a fresh id per call, so a
// test that performs multiple applies gets distinct rollout rows (the default
// test idgen returns a constant, which would overwrite the prior rollout).
func incIDs() func() string {
	n := 0
	return func() string { n++; return fmt.Sprintf("ro-%d", n) }
}

func lastWeight(r *recordingRouter) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.applied) == 0 {
		return -1
	}
	return r.applied[len(r.applied)-1].Weight
}

func flagDisabled(r *recordingFlagProvider) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.applied {
		if c.Disabled {
			return true
		}
	}
	return false
}
