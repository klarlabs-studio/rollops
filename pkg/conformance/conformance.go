// Package conformance is the shared contract test every Rollops target must
// pass — first-party (Kubernetes, SSH/VM, FTP) and community gRPC plugins
// alike. It is what keeps "infrastructure-agnostic" from meaning
// "inconsistent": a target is only correct if it is idempotent, reports a
// stable fingerprint, and gives meaningful health.
//
// Targets call Run from their _test.go:
//
//	func TestConformance(t *testing.T) {
//	    conformance.Run(t, func() (target.Target, error) { return newSSHTarget(cfg) }, sampleManifest)
//	}
//
// The individual Check* functions return errors so they can also be composed
// outside the testing harness (e.g. a CLI `rollops target verify`).
package conformance

import (
	"context"
	"fmt"
	"testing"

	"go.klarlabs.de/rollops/pkg/target"
)

// Factory returns a fresh, bound target instance.
type Factory func() (target.Target, error)

// Run executes the full conformance suite as subtests against a fresh target
// per check.
func Run(t *testing.T, newTarget Factory, sample target.Manifest) {
	t.Helper()
	ctx := context.Background()

	t.Run("Idempotent", func(t *testing.T) {
		tgt, err := newTarget()
		if err != nil {
			t.Fatalf("construct target: %v", err)
		}
		if err := CheckIdempotent(ctx, tgt, sample); err != nil {
			t.Error(err)
		}
	})
	t.Run("FingerprintStable", func(t *testing.T) {
		tgt, err := newTarget()
		if err != nil {
			t.Fatalf("construct target: %v", err)
		}
		if _, err := tgt.Apply(ctx, sample); err != nil {
			t.Fatalf("apply before observe: %v", err)
		}
		if err := CheckFingerprintStable(ctx, tgt); err != nil {
			t.Error(err)
		}
	})
	t.Run("Health", func(t *testing.T) {
		tgt, err := newTarget()
		if err != nil {
			t.Fatalf("construct target: %v", err)
		}
		if err := CheckHealth(ctx, tgt); err != nil {
			t.Error(err)
		}
	})
}

// CheckIdempotent verifies that applying the same desired state twice is a
// no-op the second time, and that after apply the observed fingerprint reflects
// the desired checksum.
func CheckIdempotent(ctx context.Context, tgt target.Target, m target.Manifest) error {
	if _, err := tgt.Apply(ctx, m); err != nil {
		return fmt.Errorf("conformance: first apply failed: %w", err)
	}
	fp, err := tgt.Observe(ctx)
	if err != nil {
		return fmt.Errorf("conformance: observe after apply: %w", err)
	}
	if m.Checksum != "" && fp.Value != m.Checksum {
		return fmt.Errorf("conformance: after apply, observed fingerprint %q != desired checksum %q", fp.Value, m.Checksum)
	}
	res, err := tgt.Apply(ctx, m)
	if err != nil {
		return fmt.Errorf("conformance: second apply failed: %w", err)
	}
	if res.Changed {
		return fmt.Errorf("conformance: re-applying identical desired state reported Changed=true; Apply must be idempotent")
	}
	return nil
}

// CheckFingerprintStable verifies Observe returns the same fingerprint across
// repeated calls when nothing changed.
func CheckFingerprintStable(ctx context.Context, tgt target.Target) error {
	first, err := tgt.Observe(ctx)
	if err != nil {
		return fmt.Errorf("conformance: observe (1): %w", err)
	}
	second, err := tgt.Observe(ctx)
	if err != nil {
		return fmt.Errorf("conformance: observe (2): %w", err)
	}
	if first.Value != second.Value {
		return fmt.Errorf("conformance: fingerprint unstable: %q != %q across consecutive observes", first.Value, second.Value)
	}
	return nil
}

// CheckHealth verifies Health reports a concrete state, not Unknown.
func CheckHealth(ctx context.Context, tgt target.Target) error {
	hs, err := tgt.Health(ctx)
	if err != nil {
		return fmt.Errorf("conformance: health: %w", err)
	}
	if hs.State == target.HealthUnknown {
		return fmt.Errorf("conformance: Health returned HealthUnknown; a target must report a concrete liveness state")
	}
	return nil
}
