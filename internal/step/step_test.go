package step

import (
	"context"
	"errors"
	"testing"
	"time"

	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// flakyTarget fails the first failFor Apply calls, then succeeds.
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

func TestGuarded_IsTarget(t *testing.T) {
	var _ pt.Target = Wrap(&flakyTarget{}, DefaultPolicy())
}

func TestApply_RetriesTransientThenSucceeds(t *testing.T) {
	f := &flakyTarget{failFor: 2, err: errTransient}
	g := Wrap(f, fastPolicy(3, 5))
	res, err := g.Apply(context.Background(), pt.Manifest{})
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
	g := Wrap(f, fastPolicy(3, 5))
	fp, err := g.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if fp.Value != "ok" {
		t.Errorf("fp = %q", fp.Value)
	}
}

func TestApply_CircuitOpensAfterRepeatedFailure(t *testing.T) {
	f := &flakyTarget{failFor: 1 << 30, err: errTransient} // always fails
	g := Wrap(f, fastPolicy(1, 2))                         // 1 attempt, trip after 2 consecutive failures

	// Two failing applies push consecutive failures to the threshold.
	_, _ = g.Apply(context.Background(), pt.Manifest{})
	_, _ = g.Apply(context.Background(), pt.Manifest{})
	callsBefore := f.calls

	// Next call should be short-circuited by the open breaker.
	_, err := g.Apply(context.Background(), pt.Manifest{})
	if err == nil {
		t.Fatal("expected ErrCircuitOpen once the breaker trips")
	}
	if f.calls != callsBefore {
		t.Errorf("breaker should short-circuit: calls went %d -> %d", callsBefore, f.calls)
	}
}
