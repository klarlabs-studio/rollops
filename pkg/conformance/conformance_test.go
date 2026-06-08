package conformance

import (
	"context"
	"testing"

	"go.klarlabs.de/rolloffs/pkg/target"
)

var sample = target.Manifest{Kind: "fake", Spec: []byte(`{"v":1}`), Checksum: "sum-v1"}

// compliantTarget is a correct stamped target: idempotent, stable fingerprint,
// concrete health.
type compliantTarget struct{ current string }

func (f *compliantTarget) Apply(_ context.Context, m target.Manifest) (target.Result, error) {
	changed := f.current != m.Checksum
	f.current = m.Checksum
	return target.Result{Changed: changed}, nil
}
func (f *compliantTarget) Observe(context.Context) (target.Fingerprint, error) {
	return target.Fingerprint{Value: f.current}, nil
}
func (f *compliantTarget) Health(context.Context) (target.HealthStatus, error) {
	return target.HealthStatus{State: target.HealthHealthy}, nil
}

func TestRun_CompliantPasses(t *testing.T) {
	Run(t, func() (target.Target, error) { return &compliantTarget{}, nil }, sample)
}

func TestCheckIdempotent_CatchesNonIdempotent(t *testing.T) {
	bad := &nonIdempotent{}
	if err := CheckIdempotent(context.Background(), bad, sample); err == nil {
		t.Fatal("expected idempotency violation to be caught")
	}
	if err := CheckIdempotent(context.Background(), &compliantTarget{}, sample); err != nil {
		t.Fatalf("compliant target failed idempotency: %v", err)
	}
}

func TestCheckFingerprintStable_CatchesDrift(t *testing.T) {
	if err := CheckFingerprintStable(context.Background(), &unstableFP{}); err == nil {
		t.Fatal("expected unstable fingerprint to be caught")
	}
}

func TestCheckHealth_CatchesUnknown(t *testing.T) {
	if err := CheckHealth(context.Background(), &unknownHealth{}); err == nil {
		t.Fatal("expected HealthUnknown to be caught")
	}
}

// --- violating targets ---

type nonIdempotent struct{}

func (nonIdempotent) Apply(context.Context, target.Manifest) (target.Result, error) {
	return target.Result{Changed: true}, nil // always claims change
}
func (nonIdempotent) Observe(context.Context) (target.Fingerprint, error) {
	return target.Fingerprint{Value: "sum-v1"}, nil
}
func (nonIdempotent) Health(context.Context) (target.HealthStatus, error) {
	return target.HealthStatus{State: target.HealthHealthy}, nil
}

type unstableFP struct{ n int }

func (f *unstableFP) Apply(context.Context, target.Manifest) (target.Result, error) {
	return target.Result{}, nil
}
func (f *unstableFP) Observe(context.Context) (target.Fingerprint, error) {
	f.n++
	return target.Fingerprint{Value: string(rune('a' + f.n))}, nil
}
func (f *unstableFP) Health(context.Context) (target.HealthStatus, error) {
	return target.HealthStatus{State: target.HealthHealthy}, nil
}

type unknownHealth struct{}

func (unknownHealth) Apply(context.Context, target.Manifest) (target.Result, error) {
	return target.Result{}, nil
}
func (unknownHealth) Observe(context.Context) (target.Fingerprint, error) {
	return target.Fingerprint{}, nil
}
func (unknownHealth) Health(context.Context) (target.HealthStatus, error) {
	return target.HealthStatus{State: target.HealthUnknown}, nil
}
