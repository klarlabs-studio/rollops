package flagconformance

import (
	"context"
	"fmt"
	"testing"

	"go.klarlabs.de/rollops/pkg/plugin"
)

var sample = plugin.FlagChange{Flag: "checkout", Environment: "production"}

// compliant is a correct in-memory provider: it threads context, accepts the
// full range, and tolerates repeats.
type compliant struct {
	last map[string]int
}

func newCompliant() *compliant { return &compliant{last: map[string]int{}} }

func (c *compliant) ApplyFlag(ctx context.Context, ch plugin.FlagChange) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.last[ch.Flag] = ch.Percentage
	return nil
}

func TestRun_CompliantPasses(t *testing.T) {
	Run(t, func() (plugin.FlagProvider, error) { return newCompliant(), nil }, sample)
}

// rangeBreaker rejects the 100% boundary.
type rangeBreaker struct{ compliant }

func (r *rangeBreaker) ApplyFlag(ctx context.Context, ch plugin.FlagChange) error {
	if ch.Percentage == 100 {
		return fmt.Errorf("cannot set 100%%")
	}
	return r.compliant.ApplyFlag(ctx, ch)
}

func TestCheckCanaryRange_CatchesBoundaryRejection(t *testing.T) {
	if err := CheckCanaryRange(context.Background(), &rangeBreaker{compliant{last: map[string]int{}}}, sample); err == nil {
		t.Fatal("expected a provider rejecting 100% to fail the range check")
	}
	if err := CheckCanaryRange(context.Background(), newCompliant(), sample); err != nil {
		t.Fatalf("compliant provider failed range check: %v", err)
	}
}

// ctxIgnorer never checks the context.
type ctxIgnorer struct{}

func (ctxIgnorer) ApplyFlag(context.Context, plugin.FlagChange) error { return nil }

func TestCheckHonorsContext_CatchesIgnoredContext(t *testing.T) {
	if err := CheckHonorsContext(ctxIgnorer{}, sample); err == nil {
		t.Fatal("expected a provider ignoring context to be caught")
	}
	if err := CheckHonorsContext(newCompliant(), sample); err != nil {
		t.Fatalf("compliant provider failed context check: %v", err)
	}
}

// rejectRepeat errors on the second apply of the same flag.
type rejectRepeat struct{ seen bool }

func (r *rejectRepeat) ApplyFlag(_ context.Context, _ plugin.FlagChange) error {
	if r.seen {
		return fmt.Errorf("already applied")
	}
	r.seen = true
	return nil
}

func TestCheckIdempotent_CatchesRepeatRejection(t *testing.T) {
	if err := CheckIdempotent(context.Background(), &rejectRepeat{}, sample); err == nil {
		t.Fatal("expected a provider rejecting a repeat apply to be caught")
	}
}

// disabledRejecter errors when asked to disable.
type disabledRejecter struct{ compliant }

func (d *disabledRejecter) ApplyFlag(ctx context.Context, ch plugin.FlagChange) error {
	if ch.Disabled {
		return fmt.Errorf("cannot disable")
	}
	return d.compliant.ApplyFlag(ctx, ch)
}

func TestCheckDisabled_CatchesDisableRejection(t *testing.T) {
	if err := CheckDisabled(context.Background(), &disabledRejecter{compliant{last: map[string]int{}}}, sample); err == nil {
		t.Fatal("expected a provider rejecting disable to be caught")
	}
}
