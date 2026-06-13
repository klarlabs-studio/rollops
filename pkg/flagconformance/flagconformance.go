// Package flagconformance is the shared contract test every Rollops feature-flag
// provider plugin should pass. Where pkg/conformance keeps targets consistent,
// this keeps flag providers consistent: a provider is only correct if it accepts
// the full canary percentage range, tolerates repeated identical applies, drives
// the disabled state, and honors context cancellation.
//
// Providers call Run from their _test.go, supplying a factory that wires the
// provider to a fake backend (its own httptest server) and a sample FlagChange
// naming a flag/environment that backend knows:
//
//	func TestConformance(t *testing.T) {
//	    flagconformance.Run(t, func() (plugin.FlagProvider, error) {
//	        srv := newFakeBackend(t)
//	        return Provider{BaseURL: srv.URL, Token: "x", HTTP: srv.Client()}, nil
//	    }, plugin.FlagChange{Flag: "checkout", Environment: "production"})
//	}
//
// The Check* functions return errors so they can also be composed outside the
// testing harness.
package flagconformance

import (
	"context"
	"fmt"
	"testing"

	"go.klarlabs.de/rollops/pkg/plugin"
)

// Factory returns a fresh provider bound to a working (fake) backend.
type Factory func() (plugin.FlagProvider, error)

// canaryRange is the sequence of percentages a provider must accept — the
// boundaries plus representative interior steps a rollout actually drives.
var canaryRange = []int{0, 1, 25, 50, 100}

// Run executes the full flag-provider conformance suite as subtests against a
// fresh provider per check.
func Run(t *testing.T, newProvider Factory, sample plugin.FlagChange) {
	t.Helper()
	ctx := context.Background()

	t.Run("CanaryRange", func(t *testing.T) {
		p := construct(t, newProvider)
		if err := CheckCanaryRange(ctx, p, sample); err != nil {
			t.Error(err)
		}
	})
	t.Run("Idempotent", func(t *testing.T) {
		p := construct(t, newProvider)
		if err := CheckIdempotent(ctx, p, sample); err != nil {
			t.Error(err)
		}
	})
	t.Run("Disabled", func(t *testing.T) {
		p := construct(t, newProvider)
		if err := CheckDisabled(ctx, p, sample); err != nil {
			t.Error(err)
		}
	})
	t.Run("HonorsContext", func(t *testing.T) {
		p := construct(t, newProvider)
		if err := CheckHonorsContext(p, sample); err != nil {
			t.Error(err)
		}
	})
}

func construct(t *testing.T, newProvider Factory) plugin.FlagProvider {
	t.Helper()
	p, err := newProvider()
	if err != nil {
		t.Fatalf("construct provider: %v", err)
	}
	return p
}

// with returns a copy of sample carrying the given percentage and disabled flag.
func with(sample plugin.FlagChange, pct int, disabled bool) plugin.FlagChange {
	sample.Percentage = pct
	sample.Disabled = disabled
	return sample
}

// CheckCanaryRange verifies the provider accepts every percentage a rollout
// drives, including the 0 and 100 boundaries.
func CheckCanaryRange(ctx context.Context, p plugin.FlagProvider, sample plugin.FlagChange) error {
	for _, pct := range canaryRange {
		if err := p.ApplyFlag(ctx, with(sample, pct, false)); err != nil {
			return fmt.Errorf("flagconformance: ApplyFlag at %d%% failed; a provider must accept the full 0-100 canary range: %w", pct, err)
		}
	}
	return nil
}

// CheckIdempotent verifies applying the same change twice is not an error — a
// provider must not reject a step Rollops legitimately repeats (retry, re-sync).
func CheckIdempotent(ctx context.Context, p plugin.FlagProvider, sample plugin.FlagChange) error {
	c := with(sample, 50, false)
	if err := p.ApplyFlag(ctx, c); err != nil {
		return fmt.Errorf("flagconformance: first ApplyFlag failed: %w", err)
	}
	if err := p.ApplyFlag(ctx, c); err != nil {
		return fmt.Errorf("flagconformance: re-applying the identical change failed; ApplyFlag must be idempotent: %w", err)
	}
	return nil
}

// CheckDisabled verifies the provider can drive the flag's disabled state, which
// Rollops uses on rollback.
func CheckDisabled(ctx context.Context, p plugin.FlagProvider, sample plugin.FlagChange) error {
	if err := p.ApplyFlag(ctx, with(sample, 0, true)); err != nil {
		return fmt.Errorf("flagconformance: ApplyFlag with Disabled=true failed; a provider must drive the disabled state: %w", err)
	}
	return nil
}

// CheckHonorsContext verifies the provider propagates context cancellation
// rather than dropping the caller's context — a provider that ignores ctx cannot
// be cancelled or time-bounded by the host.
func CheckHonorsContext(p plugin.FlagProvider, sample plugin.FlagChange) error {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.ApplyFlag(ctx, with(sample, 50, false)); err == nil {
		return fmt.Errorf("flagconformance: ApplyFlag returned nil under a cancelled context; a provider must thread the context into its backend calls")
	}
	return nil
}
