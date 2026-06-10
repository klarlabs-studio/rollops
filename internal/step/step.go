// Package step wraps every target operation in fortify resilience — retry with
// exponential backoff on all three operations, plus a circuit breaker around
// the mutating Apply so a target that keeps failing is shed fast instead of
// being hammered. The result is itself a target.Target, so the engine and
// reconciler consume a guarded target transparently.
//
// (Deep axi-go capability modeling of each step — sandboxed invocation with its
// own budget — is the natural next layer here; this version delivers the
// resilience envelope the targets and rollback paths depend on.)
package step

import (
	"context"
	"time"

	"go.klarlabs.de/fortify/circuitbreaker"
	"go.klarlabs.de/fortify/retry"

	pt "go.klarlabs.de/rollops/pkg/target"
)

// Policy tunes the resilience envelope around a target.
type Policy struct {
	MaxAttempts      int           // total attempts including the first
	InitialDelay     time.Duration // backoff before the first retry
	MaxDelay         time.Duration // backoff ceiling
	FailureThreshold uint32        // consecutive Apply failures before the circuit opens
}

// DefaultPolicy is a sensible, lean default.
func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 3, InitialDelay: 100 * time.Millisecond, MaxDelay: 5 * time.Second, FailureThreshold: 5}
}

// Guarded decorates a target.Target with fortify retry + circuit breaker.
type Guarded struct {
	inner       pt.Target
	applyRetry  retry.Retry[pt.Result]
	obsRetry    retry.Retry[pt.Fingerprint]
	healthRetry retry.Retry[pt.HealthStatus]
	cb          circuitbreaker.CircuitBreaker[pt.Result]
}

// Wrap builds a Guarded target from inner using p.
func Wrap(inner pt.Target, p Policy) *Guarded {
	rc := retry.Config{MaxAttempts: p.MaxAttempts, InitialDelay: p.InitialDelay, MaxDelay: p.MaxDelay}
	threshold := p.FailureThreshold
	if threshold == 0 {
		threshold = 5
	}
	return &Guarded{
		inner:       inner,
		applyRetry:  retry.New[pt.Result](rc),
		obsRetry:    retry.New[pt.Fingerprint](rc),
		healthRetry: retry.New[pt.HealthStatus](rc),
		cb: circuitbreaker.New[pt.Result](circuitbreaker.Config{
			ReadyToTrip: func(c circuitbreaker.Counts) bool {
				return c.ConsecutiveFailures >= threshold
			},
		}),
	}
}

// Apply runs the mutating operation through the circuit breaker, retrying
// transient failures within it. A tripped circuit returns ErrCircuitOpen
// without touching the target.
func (g *Guarded) Apply(ctx context.Context, m pt.Manifest) (pt.Result, error) {
	return g.cb.Execute(ctx, func(ctx context.Context) (pt.Result, error) {
		return g.applyRetry.Execute(ctx, func(ctx context.Context) (pt.Result, error) {
			return g.inner.Apply(ctx, m)
		})
	})
}

// Observe retries transient read failures.
func (g *Guarded) Observe(ctx context.Context) (pt.Fingerprint, error) {
	return g.obsRetry.Execute(ctx, func(ctx context.Context) (pt.Fingerprint, error) {
		return g.inner.Observe(ctx)
	})
}

// Health retries transient probe failures.
func (g *Guarded) Health(ctx context.Context) (pt.HealthStatus, error) {
	return g.healthRetry.Execute(ctx, func(ctx context.Context) (pt.HealthStatus, error) {
		return g.inner.Health(ctx)
	})
}
