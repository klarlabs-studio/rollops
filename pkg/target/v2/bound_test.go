package targetv2_test

import (
	"context"
	"strings"
	"testing"

	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// core implements the mandatory contract and nothing else. It is what a
// minimal target looks like, and what the v1 plugin adapter effectively was.
type core struct {
	applied []targetv2.ApplyRequest
}

func (c *core) Metadata() targetv2.Metadata {
	return targetv2.Metadata{Kind: "fake", Name: "x/test/one", Version: "0.0.0"}
}

func (c *core) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return targetv2.Capabilities{DriftDetection: true}, nil
}

func (c *core) Inspect(context.Context, targetv2.InspectRequest) (targetv2.ObservedState, error) {
	return targetv2.ObservedState{Fingerprint: "live"}, nil
}

func (c *core) Plan(context.Context, targetv2.PlanRequest) (targetv2.PlanResult, error) {
	return targetv2.PlanResult{Changes: true}, nil
}

func (c *core) Apply(_ context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	c.applied = append(c.applied, req)
	return targetv2.ApplyResult{Changed: true, Handle: "h1"}, nil
}

func (c *core) Observe(context.Context, targetv2.ObserveRequest) (targetv2.Observation, error) {
	return targetv2.Observation{Fingerprint: "live"}, nil
}

func (c *core) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
}

// drifting adds the one optional capability core declares.
type drifting struct {
	core
	calls int
}

func (d *drifting) DetectDrift(context.Context, targetv2.DriftRequest) (targetv2.DriftResult, error) {
	d.calls++
	return targetv2.DriftResult{Drifted: true, Detail: "replicas 3 -> 5"}, nil
}

func TestAnUnauthorizedCapabilityNeverReachesTheTarget(t *testing.T) {
	// The manifest did not authorize drift, so the target is not asked. A
	// capability that was refused at install time must not be reachable by
	// calling the method anyway.
	tgt := &drifting{}
	b := targetv2.NewBound(tgt, targetv2.Capabilities{})

	_, err := b.DetectDrift(context.Background(), targetv2.DriftRequest{})

	if !targetv2.IsUnsupported(err) {
		t.Fatalf("DetectDrift gave %v, want an unsupported error", err)
	}
	if tgt.calls != 0 {
		t.Errorf("the target was called %d times for a capability it was not granted", tgt.calls)
	}
}

func TestAnAuthorizedCapabilityReachesTheTarget(t *testing.T) {
	tgt := &drifting{}
	b := targetv2.NewBound(tgt, targetv2.Capabilities{DriftDetection: true})

	res, err := b.DetectDrift(context.Background(), targetv2.DriftRequest{})

	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}
	if !res.Drifted || res.Detail != "replicas 3 -> 5" {
		t.Errorf("the result did not come from the target: %+v", res)
	}
}

func TestDeclaringWhatIsNotImplementedIsAFailureNotAnAbsence(t *testing.T) {
	// A target that claims drift and has no DetectDrift is broken. Reporting
	// that as "unsupported" would let it keep lying — the caller would fall
	// back and never find out. This is the one place the assertion is allowed,
	// and it is allowed because failing it is an error rather than a branch.
	b := targetv2.NewBound(&core{}, targetv2.Capabilities{DriftDetection: true})

	_, err := b.DetectDrift(context.Background(), targetv2.DriftRequest{})

	if err == nil {
		t.Fatalf("a target declaring an unimplemented capability succeeded")
	}
	if targetv2.IsUnsupported(err) {
		t.Errorf("a broken target was reported as one that simply cannot")
	}
	if got := targetv2.KindOf(err); got != targetv2.KindInternal {
		t.Errorf("kind is %q, want %q", got, targetv2.KindInternal)
	}
	if !strings.Contains(err.Error(), string(targetv2.CapabilityDriftDetection)) {
		t.Errorf("message %q does not name the capability that was lied about", err)
	}
}

func TestCanAnswersFromTheResolvedCapabilities(t *testing.T) {
	// Not from the target's own claim, and not from a type assertion: from
	// what the host resolved (ADR-0006). The target below claims drift; the
	// host granted rollback instead, and the host wins.
	b := targetv2.NewBound(&drifting{}, targetv2.Capabilities{NativeRollback: true})

	if b.Can(targetv2.CapabilityDriftDetection) {
		t.Errorf("drift was not granted but Can says yes")
	}
	if !b.Can(targetv2.CapabilityNativeRollback) {
		t.Errorf("rollback was granted but Can says no")
	}
}

func TestTheResolvedCapabilitiesAreWhatCapabilitiesReturns(t *testing.T) {
	// Callers holding a Bound must never see the unresolved claim, or the
	// narrowing the host performed is advisory.
	b := targetv2.NewBound(&drifting{}, targetv2.Capabilities{NativeRollback: true})

	caps, err := b.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps != (targetv2.Capabilities{NativeRollback: true}) {
		t.Errorf("Capabilities returned %+v, want the resolved set", caps)
	}
}

func TestApplyRefusesAnIdempotencyKeyItDoesNotHave(t *testing.T) {
	// §9.4 makes the key mandatory. It is checked here rather than in each
	// target, because a target that forgets to check is a target whose retries
	// apply twice — and the host is the side that can be made to check once.
	tgt := &drifting{}
	b := targetv2.NewBound(tgt, targetv2.Capabilities{})

	_, err := b.Apply(context.Background(), targetv2.ApplyRequest{})

	if got := targetv2.KindOf(err); got != targetv2.KindInvalid {
		t.Fatalf("an apply with no idempotency key gave kind %q, want %q", got, targetv2.KindInvalid)
	}
	if len(tgt.applied) != 0 {
		t.Errorf("the target was applied without an idempotency key")
	}
}

func TestApplyPassesTheRequestThroughUnchanged(t *testing.T) {
	tgt := &drifting{}
	b := targetv2.NewBound(tgt, targetv2.Capabilities{})
	req := targetv2.ApplyRequest{
		Desired:        targetv2.DesiredState{Kind: "fake", Checksum: "abc"},
		IdempotencyKey: "dep-1/op-1",
	}

	res, err := b.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Handle != "h1" {
		t.Errorf("handle is %q, want the target's", res.Handle)
	}
	if len(tgt.applied) != 1 || tgt.applied[0].IdempotencyKey != "dep-1/op-1" {
		t.Errorf("the target saw %+v", tgt.applied)
	}
}

func TestAnIdempotencyKeyIsStableAcrossRetriesAndUniquePerIntent(t *testing.T) {
	// Derived from durable state, so the retry after a crash mints the same
	// key the lost call used (ADR-0006). Two deployments planning an identical
	// change are two intents and get two keys.
	first := targetv2.IdempotencyKeyFor("dep-7", "op-3")
	if first != targetv2.IdempotencyKeyFor("dep-7", "op-3") {
		t.Errorf("the same operation minted two keys")
	}
	if first == targetv2.IdempotencyKeyFor("dep-8", "op-3") {
		t.Errorf("two deployments share one key")
	}
	if first == targetv2.IdempotencyKeyFor("dep-7", "op-4") {
		t.Errorf("two operations share one key")
	}
	if first == "" {
		t.Errorf("the key is empty, which Apply refuses")
	}
}
