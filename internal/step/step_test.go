package step

import (
	"context"
	"errors"
	"testing"
	"time"

	pt "go.klarlabs.de/rollops/pkg/target"
	"go.klarlabs.de/rollops/pkg/target/v1adapter"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// flakyTarget fails the first failFor Apply calls, then succeeds. It is written
// against v1 and lifted, which is how every first-party target reaches the
// engine today.
type flakyTarget struct {
	calls   int
	failFor int
	err     error
}

func (f *flakyTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	f.calls++
	if f.calls <= f.failFor {
		return pt.Result{}, f.err
	}
	return pt.Result{Changed: true}, nil
}
func (f *flakyTarget) Observe(context.Context) (pt.Fingerprint, error) {
	f.calls++
	if f.calls <= f.failFor {
		return pt.Fingerprint{}, f.err
	}
	return pt.Fingerprint{Value: "ok"}, nil
}
func (f *flakyTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

var errTransient = errors.New("transient")

func fastPolicy(maxAttempts int, threshold uint32) Policy {
	return Policy{MaxAttempts: maxAttempts, InitialDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond, FailureThreshold: threshold}
}

// bind lifts a v1 target and binds it to the capabilities the adapter derives,
// the way the registry does.
func bind(t *testing.T, inner pt.Target) *targetv2.Bound {
	t.Helper()
	a := v1adapter.New(inner, targetv2.Metadata{Kind: "fake", Name: "x/test/one", Version: "v1"})
	caps, err := a.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	return targetv2.NewBound(a, caps)
}

func applyReq() targetv2.ApplyRequest {
	return targetv2.ApplyRequest{IdempotencyKey: targetv2.IdempotencyKeyFor("rollout-1", "sum-1")}
}

func TestApply_RetriesTransientThenSucceeds(t *testing.T) {
	f := &flakyTarget{failFor: 2, err: errTransient}
	g := Wrap(bind(t, f), fastPolicy(3, 5))
	res, err := g.Apply(context.Background(), applyReq())
	if err != nil {
		t.Fatalf("expected success within retries, got: %v", err)
	}
	if !res.Changed {
		t.Error("expected Changed=true on eventual success")
	}
	if f.calls != 3 {
		t.Errorf("calls = %d, want 3 (2 fail + 1 success)", f.calls)
	}
}

func TestObserve_Retries(t *testing.T) {
	f := &flakyTarget{failFor: 1, err: errTransient}
	g := Wrap(bind(t, f), fastPolicy(3, 5))
	obs, err := g.Observe(context.Background(), targetv2.ObserveRequest{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Fingerprint != "ok" {
		t.Errorf("fingerprint = %q", obs.Fingerprint)
	}
}

func TestApply_CircuitOpensAfterRepeatedFailure(t *testing.T) {
	f := &flakyTarget{failFor: 1 << 30, err: errTransient} // always fails
	g := Wrap(bind(t, f), fastPolicy(1, 2))                // 1 attempt, trip after 2 consecutive failures

	// Two failing applies push consecutive failures to the threshold.
	_, _ = g.Apply(context.Background(), applyReq())
	_, _ = g.Apply(context.Background(), applyReq())
	callsBefore := f.calls

	// Next call should be short-circuited by the open breaker.
	_, err := g.Apply(context.Background(), applyReq())
	if err == nil {
		t.Fatal("expected ErrCircuitOpen once the breaker trips")
	}
	if f.calls != callsBefore {
		t.Errorf("breaker should short-circuit: calls went %d -> %d", callsBefore, f.calls)
	}
}

// TestApply_RefusesAMissingIdempotencyKey keeps §9.4 in force through the
// decoration. The envelope retries a mutation, so a request the target cannot
// recognize as a replay is one this layer would apply several times.
func TestApply_RefusesAMissingIdempotencyKey(t *testing.T) {
	f := &flakyTarget{}
	g := Wrap(bind(t, f), fastPolicy(3, 5))
	_, err := g.Apply(context.Background(), targetv2.ApplyRequest{})
	if got := targetv2.KindOf(err); got != targetv2.KindInvalid {
		t.Fatalf("an apply with no idempotency key gave kind %q, want %q", got, targetv2.KindInvalid)
	}
	if f.calls != 0 {
		t.Errorf("the target was applied %d times without an idempotency key", f.calls)
	}
}

// driftingTarget is a v1 target implementing one optional interface.
type driftingTarget struct {
	flakyTarget
	diffs int
}

func (d *driftingTarget) Diff(context.Context, pt.Manifest) (string, error) {
	d.diffs++
	return "replicas 3 -> 5", nil
}

// TestWrap_KeepsOptionalCapabilitiesReachable is the whole reason the envelope
// is shaped the way it is. The binding resolves an optional verb by asserting
// the target it holds, so a decoration that implements only the mandatory
// contract turns every capable target into one that appears to have lied about
// what it can do — and the caller falls back silently rather than failing.
func TestWrap_KeepsOptionalCapabilitiesReachable(t *testing.T) {
	d := &driftingTarget{}
	b := bind(t, d)
	if !b.Can(targetv2.CapabilityDriftDetection) {
		t.Fatal("the undecorated binding did not grant drift detection")
	}

	g := Wrap(b, DefaultPolicy())

	if !g.Can(targetv2.CapabilityDriftDetection) {
		t.Fatal("the decoration dropped the capability from the binding")
	}
	res, err := g.DetectDrift(context.Background(), targetv2.DriftRequest{})
	if err != nil {
		t.Fatalf("DetectDrift through the envelope: %v", err)
	}
	if res.Detail != "replicas 3 -> 5" {
		t.Errorf("the result did not come from the target: %+v", res)
	}
	if d.diffs != 1 {
		t.Errorf("the target was asked %d times, want 1", d.diffs)
	}
}

// TestWrap_RefusesACapabilityTheBindingDidNotGrant keeps the narrowing in force
// across the decoration. Re-resolving capabilities from the target here would
// hand back what the operator refused.
func TestWrap_RefusesACapabilityTheBindingDidNotGrant(t *testing.T) {
	d := &driftingTarget{}
	a := v1adapter.New(d, targetv2.Metadata{Kind: "fake", Name: "x/test/one", Version: "v1"})
	g := Wrap(targetv2.NewBound(a, targetv2.Capabilities{}), DefaultPolicy())

	if _, err := g.DetectDrift(context.Background(), targetv2.DriftRequest{}); !targetv2.IsUnsupported(err) {
		t.Fatalf("DetectDrift gave %v, want an unsupported error", err)
	}
	if d.diffs != 0 {
		t.Errorf("the target was asked %d times for a capability it was not granted", d.diffs)
	}
}

type closableTarget struct {
	flakyTarget
	closed bool
}

func (c *closableTarget) Close() error { c.closed = true; return nil }

func TestClose_ForwardsToCloserInner(t *testing.T) {
	ct := &closableTarget{}
	if err := Wrap(bind(t, ct), DefaultPolicy()).Close(); err != nil {
		t.Fatal(err)
	}
	if !ct.closed {
		t.Error("Close must forward to the inner target")
	}
	if err := Wrap(bind(t, &flakyTarget{}), DefaultPolicy()).Close(); err != nil {
		t.Errorf("Close on non-closer inner must be a no-op, got %v", err)
	}
}

// TestClose_ReleasesWhatTheBindingHeld keeps a plugin subprocess from outliving
// the caller. The release step belongs to the binding the envelope decorates,
// so an envelope that closed only the target would leak the process — silently,
// because a leaked process still answers.
func TestClose_ReleasesWhatTheBindingHeld(t *testing.T) {
	released := 0
	a := v1adapter.New(&flakyTarget{}, targetv2.Metadata{Kind: "fake", Name: "x/test/one", Version: "v1"})
	b := targetv2.NewBound(a, targetv2.Capabilities{}, targetv2.OnClose(func() error { released++; return nil }))

	if err := Wrap(b, DefaultPolicy()).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if released != 1 {
		t.Errorf("the release step ran %d times, want 1", released)
	}
}
