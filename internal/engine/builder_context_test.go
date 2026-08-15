package engine

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/internal/analysis"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/trafficrouting"
)

// All three plugin builders — feature flags, traffic routing, metrics — hardcoded
// context.Background() while the engine call sites that invoke them had a ctx in scope
// and dropped it. So a rollout that was cancelled still waited out each plugin's own
// timeouts on a subprocess nobody was waiting for. #113 fixed the feature-flag builder
// and missed the other two.
//
// Asserted here rather than in each builder's own package because the engine is where the
// context originates: these seams can observe exactly what they are handed, which is the
// behaviour that regressed.

type ctxKey string

const marker ctxKey = "engine-context-marker"

// carriesMarker reports whether a context descends from the one the test passed in.
// Identity is what matters: a builder handed context.Background() instead would carry no
// marker, which is precisely the bug.
func carriesMarker(ctx context.Context) bool {
	return ctx != nil && ctx.Value(marker) == "yes"
}

func TestTheTrafficRouterBuilderReceivesTheCallersContext(t *testing.T) {
	fake := &fakeTarget{}
	var got context.Context

	e, _ := newEngine(t, fake, WithTrafficRouterBuilder(
		func(ctx context.Context, _ *config.TrafficRouting) (trafficrouting.Router, error) {
			got = ctx
			return nil, context.Canceled // no router needed; the context is the subject
		}))

	cfg := loadConfig(t)
	cfg.Spec.TrafficRouting = &config.TrafficRouting{Provider: "gateway"}

	ctx := context.WithValue(context.Background(), marker, "yes")
	_ = e.driveTraffic(ctx, "demo/prod/app", cfg.Spec.TrafficRouting, 50)

	if got == nil {
		t.Fatal("the traffic-router builder was never called")
	}
	if !carriesMarker(got) {
		t.Error("the builder was handed a context that does not descend from the caller's: " +
			"a cancelled rollout cannot then interrupt launching the router plugin")
	}
}

func TestTheMetricsBuilderReceivesTheCallersContext(t *testing.T) {
	fake := &fakeTarget{}
	var got context.Context

	e, _ := newEngine(t, fake, WithMetricsProviderBuilder(
		func(ctx context.Context, _ *config.Analysis) (analysis.MetricsProvider, error) {
			got = ctx
			return nil, context.Canceled
		}))

	ctx := context.WithValue(context.Background(), marker, "yes")
	_, _ = e.runAnalysis(ctx, &config.Analysis{Plugin: "/nonexistent/metrics-plugin"})

	if got == nil {
		t.Fatal("the metrics builder was never called")
	}
	if !carriesMarker(got) {
		t.Error("the builder was handed a context that does not descend from the caller's: " +
			"an abandoned analysis cannot then interrupt launching the metrics plugin")
	}
}

// A cancelled caller must be observable by the builder, which is the point of threading
// the context rather than merely passing one.
func TestACancelledRolloutIsVisibleToTheTrafficRouterBuilder(t *testing.T) {
	fake := &fakeTarget{}
	var seenErr error

	e, _ := newEngine(t, fake, WithTrafficRouterBuilder(
		func(ctx context.Context, _ *config.TrafficRouting) (trafficrouting.Router, error) {
			seenErr = ctx.Err()
			return nil, context.Canceled
		}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := e.driveTraffic(ctx, "demo/prod/app", &config.TrafficRouting{Provider: "gateway"}, 50); err == nil {
		t.Fatal("canceled driveTraffic must fail")
	}

	if seenErr == nil {
		t.Error("the builder saw a live context after the caller cancelled: it would launch " +
			"a plugin subprocess for a rollout that has already been abandoned")
	}
}
