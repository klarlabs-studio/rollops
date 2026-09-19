// Package step wraps every target operation in fortify resilience — retry with
// exponential backoff on the reads, plus a circuit breaker around the mutating
// Apply so a target that keeps failing is shed fast instead of being hammered.
// The result is another binding, so the engine and reconciler consume a guarded
// target transparently.
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

	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
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

// guarded decorates a target with fortify retry and a circuit breaker. It is
// unexported because nothing may hold one directly: the capability check lives
// on the binding, so a caller holding the decorated target could reach a verb
// the operator refused.
type guarded struct {
	bound        *targetv2.Bound
	inner        targetv2.Target
	applyRetry   retry.Retry[targetv2.ApplyResult]
	inspectRetry retry.Retry[targetv2.ObservedState]
	planRetry    retry.Retry[targetv2.PlanResult]
	observeRetry retry.Retry[targetv2.Observation]
	cb           circuitbreaker.CircuitBreaker[targetv2.ApplyResult]
}

// Wrap returns a binding whose operations run inside the resilience envelope,
// carrying over the capabilities b already resolved. Re-resolving them here
// would ask the target a second question it has already answered, and the
// answer that matters is the one the host narrowed.
func Wrap(b *targetv2.Bound, p Policy) *targetv2.Bound {
	rc := retry.Config{MaxAttempts: p.MaxAttempts, InitialDelay: p.InitialDelay, MaxDelay: p.MaxDelay}
	threshold := p.FailureThreshold
	if threshold == 0 {
		threshold = 5
	}
	caps, _ := b.Capabilities(context.Background())
	g := &guarded{
		bound:        b,
		inner:        b.Unwrap(),
		applyRetry:   retry.New[targetv2.ApplyResult](rc),
		inspectRetry: retry.New[targetv2.ObservedState](rc),
		planRetry:    retry.New[targetv2.PlanResult](rc),
		observeRetry: retry.New[targetv2.Observation](rc),
		cb: circuitbreaker.New[targetv2.ApplyResult](circuitbreaker.Config{
			ReadyToTrip: func(c circuitbreaker.Counts) bool {
				return c.ConsecutiveFailures >= threshold
			},
		}),
	}
	return targetv2.NewBound(g, caps)
}

// Metadata identifies the target the envelope is around, not the envelope.
func (g *guarded) Metadata() targetv2.Metadata { return g.inner.Metadata() }

// Capabilities passes through. Retrying it would delay the answer to "may this
// target do anything at all", which no caller is waiting on.
func (g *guarded) Capabilities(ctx context.Context) (targetv2.Capabilities, error) {
	return g.inner.Capabilities(ctx)
}

// Inspect retries transient read failures.
func (g *guarded) Inspect(ctx context.Context, req targetv2.InspectRequest) (targetv2.ObservedState, error) {
	return g.inspectRetry.Execute(ctx, func(ctx context.Context) (targetv2.ObservedState, error) {
		return g.inner.Inspect(ctx, req)
	})
}

// Plan retries transient failures. Retrying is safe here only because Plan has
// no side effects (§9.5); a target that breaks that axis turns this into a
// mutation repeated up to MaxAttempts times.
func (g *guarded) Plan(ctx context.Context, req targetv2.PlanRequest) (targetv2.PlanResult, error) {
	return g.planRetry.Execute(ctx, func(ctx context.Context) (targetv2.PlanResult, error) {
		return g.inner.Plan(ctx, req)
	})
}

// Apply runs the mutating operation through the circuit breaker, retrying
// transient failures within it. A tripped circuit returns ErrCircuitOpen
// without touching the target. Retrying a mutation is only safe because the
// request carries an idempotency key the target replays against (§9.4).
func (g *guarded) Apply(ctx context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	return g.cb.Execute(ctx, func(ctx context.Context) (targetv2.ApplyResult, error) {
		return g.applyRetry.Execute(ctx, func(ctx context.Context) (targetv2.ApplyResult, error) {
			return g.inner.Apply(ctx, req)
		})
	})
}

// Observe retries transient probe failures.
func (g *guarded) Observe(ctx context.Context, req targetv2.ObserveRequest) (targetv2.Observation, error) {
	return g.observeRetry.Execute(ctx, func(ctx context.Context) (targetv2.Observation, error) {
		return g.inner.Observe(ctx, req)
	})
}

// Rollback goes straight to the target. It is the escape from a failing apply,
// and the breaker that tripped on those applies is the one thing that must not
// stand between an operator and the undo.
func (g *guarded) Rollback(ctx context.Context, req targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return g.inner.Rollback(ctx, req)
}

// The optional verbs are forwarded rather than left out. A decoration that
// implements only the mandatory contract is how a capability goes missing: the
// binding resolves the optional verb by asserting the target it holds, and a
// wrapper that does not satisfy the subinterface makes a target that declares
// drift look like one that lied about it.
func (g *guarded) Promote(ctx context.Context, req targetv2.PromoteRequest) (targetv2.PromoteResult, error) {
	impl, err := optional[targetv2.Promoter](g, "Promote", targetv2.CapabilityProgressiveDelivery)
	if err != nil {
		return targetv2.PromoteResult{}, err
	}
	return impl.Promote(ctx, req)
}

func (g *guarded) DetectDrift(ctx context.Context, req targetv2.DriftRequest) (targetv2.DriftResult, error) {
	impl, err := optional[targetv2.Drifter](g, "DetectDrift", targetv2.CapabilityDriftDetection)
	if err != nil {
		return targetv2.DriftResult{}, err
	}
	return impl.DetectDrift(ctx, req)
}

func (g *guarded) Prune(ctx context.Context, req targetv2.PruneRequest) (targetv2.PruneResult, error) {
	impl, err := optional[targetv2.Pruner](g, "Prune", targetv2.CapabilityPrune)
	if err != nil {
		return targetv2.PruneResult{}, err
	}
	return impl.Prune(ctx, req)
}

// optional resolves a subinterface on the decorated target. The binding has
// already checked the capability by the time this runs, so a miss here means
// the target declared something it does not implement — an error rather than
// an absence, for the same reason it is one on the binding.
func optional[T any](g *guarded, op string, c targetv2.Capability) (T, error) {
	var zero T
	impl, ok := g.inner.(T)
	if !ok {
		return zero, targetv2.Failf(targetv2.KindInternal, op, nil,
			"target %s declares %s but does not implement it", g.inner.Metadata().Name, c)
	}
	return impl, nil
}

// Close releases through the binding it decorates rather than through the bare
// target, because a plugin's subprocess is held by the binding's release step
// and closing only the target would leave the process running.
func (g *guarded) Close() error { return g.bound.Close() }
