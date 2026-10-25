package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/step"
	pt "go.klarlabs.de/rollops/pkg/target"
)

var errAPIDown = errors.New("kubernetes: health: Unable to connect to the server: connection refused")

// errs returns n copies of errAPIDown. The step.Guarded wrapper that
// buildTarget adds already retries each Health call 3 times within a
// second; these tests outlast it to exercise the engine's slower retry,
// which covers an API-server restart.
func errs(n int) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = errAPIDown
	}
	return out
}

func TestGateHealth_RetriesATransientError(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}, healthErrs: errs(5)}
	e, _ := newEngine(t, fake, WithHealthRetryDelay(0), WithPolicy(fastRetry()))
	got := e.gateHealth(context.Background(), rollout.Rollout{TargetRef: "demo/prod/app", Desired: pt.Manifest{Kind: "fake"}})
	if got.Status != GatePass {
		t.Fatalf("gate = %+v; want a pass after two transient errors", got)
	}
}

func TestGateHealth_FailsWhenTheErrorPersists(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}, healthErrs: errs(50)}
	e, _ := newEngine(t, fake, WithHealthRetryDelay(0), WithPolicy(fastRetry()))
	got := e.gateHealth(context.Background(), rollout.Rollout{TargetRef: "demo/prod/app", Desired: pt.Manifest{Kind: "fake"}})
	if got.Status != GateFail {
		t.Fatalf("gate = %+v; want a fail when the health check never answers", got)
	}
}

func TestStepHealth_RetriesATransientError(t *testing.T) {
	fake := &fakeTarget{health: pt.HealthStatus{State: pt.HealthHealthy}, healthErrs: []error{errAPIDown}}
	e, _ := newEngine(t, fake, WithHealthRetryDelay(0))
	var healthErr error
	if !e.stepHealth(context.Background(), fake, &healthErr)(0, 50) {
		t.Fatalf("step health failed on one transient error: %v", healthErr)
	}
}

// fastRetry keeps the step.Guarded retries but without their real delays.
func fastRetry() step.Policy {
	return step.Policy{MaxAttempts: 3, InitialDelay: time.Millisecond, MaxDelay: time.Millisecond, FailureThreshold: 5}
}
