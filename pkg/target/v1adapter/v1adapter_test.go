package v1adapter_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	v1 "go.klarlabs.de/rollops/pkg/target"
	"go.klarlabs.de/rollops/pkg/target/v1adapter"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// bare implements only the v1 mandatory contract.
type bare struct {
	applies int
	live    string
}

func (b *bare) Apply(_ context.Context, m v1.Manifest) (v1.Result, error) {
	b.applies++
	changed := b.live != m.Checksum
	b.live = m.Checksum
	return v1.Result{Changed: changed, Detail: "applied " + m.Checksum}, nil
}

func (b *bare) Observe(context.Context) (v1.Fingerprint, error) {
	return v1.Fingerprint{Value: b.live}, nil
}

func (b *bare) Health(context.Context) (v1.HealthStatus, error) {
	return v1.HealthStatus{State: v1.HealthHealthy, Reason: "all ready"}, nil
}

// rich adds every optional v1 capability.
type rich struct {
	bare
	diff         string
	preflightErr error
	reaped       int
}

func (r *rich) Diff(context.Context, v1.Manifest) (string, error) { return r.diff, nil }

func (r *rich) Preflight(context.Context, v1.Manifest) error { return r.preflightErr }

func (r *rich) Render(_ context.Context, m v1.Manifest) ([]byte, error) {
	return append([]byte("rendered:"), m.Spec...), nil
}

func (r *rich) Referenced(v1.Manifest) bool { return true }

func (r *rich) Resources(context.Context) ([]v1.Resource, error) {
	return []v1.Resource{{Kind: "Deployment", Name: "api", Status: "ready"}}, nil
}

func (r *rich) ReapTarget(context.Context) (int, error) {
	r.reaped++
	return 3, nil
}

func meta() targetv2.Metadata {
	return targetv2.Metadata{Kind: "fake", Name: "x/test/one", Version: "1"}
}

func desired(checksum string) targetv2.DesiredState {
	return targetv2.DesiredState{Kind: "fake", Spec: []byte("spec"), Checksum: checksum}
}

func TestCapabilitiesComeFromWhatTheV1TargetImplements(t *testing.T) {
	// The one place a type assertion is still right: both sides are in this
	// process, on a value this code holds. That is exactly what is not true
	// across a plugin boundary.
	ctx := context.Background()

	thin, err := v1adapter.New(&bare{}, meta()).Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if thin != (targetv2.Capabilities{HealthObservation: true}) {
		t.Errorf("a bare v1 target reports %+v, want health observation only", thin)
	}

	full, err := v1adapter.New(&rich{}, meta()).Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	want := targetv2.Capabilities{HealthObservation: true, DriftDetection: true, Prune: true}
	if full != want {
		t.Errorf("a rich v1 target reports %+v, want %+v", full, want)
	}
}

func TestNoV1TargetRollsBackNatively(t *testing.T) {
	// v1 has no rollback at all; the engine rolls back by applying the
	// previous state. Saying so is what lets the engine keep doing that.
	a := v1adapter.New(&rich{}, meta())

	_, err := a.Rollback(context.Background(), targetv2.RollbackRequest{})

	if !targetv2.IsUnsupported(err) {
		t.Fatalf("Rollback gave %v, want unsupported", err)
	}
}

func TestAnAbsentCapabilityIsUnsupportedRatherThanBroken(t *testing.T) {
	a := v1adapter.New(&bare{}, meta())

	_, err := a.DetectDrift(context.Background(), targetv2.DriftRequest{Desired: desired("abc")})

	if !targetv2.IsUnsupported(err) {
		t.Fatalf("DetectDrift on a target that cannot diff gave %v, want unsupported", err)
	}
}

func TestTheSameKeyReplaysRatherThanApplyingTwice(t *testing.T) {
	// §9.4's first branch. The case is a crash between the call and its
	// response: the retry carries the same key, and applying again would be a
	// second apply the caller never asked for.
	tgt := &bare{}
	a := v1adapter.New(tgt, meta())
	req := targetv2.ApplyRequest{Desired: desired("abc"), IdempotencyKey: "dep-1/op-1"}

	first, err := a.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second, err := a.Apply(context.Background(), req)
	if err != nil {
		t.Fatalf("replayed apply: %v", err)
	}

	if tgt.applies != 1 {
		t.Errorf("the v1 target was applied %d times for one key", tgt.applies)
	}
	if second != first {
		t.Errorf("the replay returned %+v, want the first result %+v", second, first)
	}
}

func TestTheSameKeyWithADifferentRequestIsAConflict(t *testing.T) {
	// §9.4's second branch. Two different requests under one key means the
	// caller's bookkeeping is wrong, and guessing which one it meant is worse
	// than refusing.
	tgt := &bare{}
	a := v1adapter.New(tgt, meta())

	if _, err := a.Apply(context.Background(), targetv2.ApplyRequest{
		Desired: desired("abc"), IdempotencyKey: "dep-1/op-1",
	}); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	_, err := a.Apply(context.Background(), targetv2.ApplyRequest{
		Desired: desired("def"), IdempotencyKey: "dep-1/op-1",
	})

	if got := targetv2.KindOf(err); got != targetv2.KindConflict {
		t.Fatalf("reusing a key gave kind %q, want %q", got, targetv2.KindConflict)
	}
	var te *targetv2.Error
	if errors.As(err, &te) && te.IdempotencyKey != "dep-1/op-1" {
		t.Errorf("the conflict names key %q, want dep-1/op-1", te.IdempotencyKey)
	}
	if tgt.applies != 1 {
		t.Errorf("the conflicting request was applied anyway")
	}
}

func TestPlanLooksWithoutTouching(t *testing.T) {
	// §9.5: a plan with side effects has already half-applied the batch it
	// was meant to guard.
	tgt := &rich{diff: "- replicas: 3\n+ replicas: 5"}
	a := v1adapter.New(tgt, meta())

	res, err := a.Plan(context.Background(), targetv2.PlanRequest{Desired: desired("abc")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	if tgt.applies != 0 {
		t.Errorf("Plan applied %d times", tgt.applies)
	}
	if !res.Changes {
		t.Errorf("a non-empty diff planned no change")
	}
	if !strings.Contains(res.Diff, "replicas") {
		t.Errorf("the diff did not reach the plan: %q", res.Diff)
	}
	if string(res.Rendered) != "rendered:spec" {
		t.Errorf("rendered is %q, want the v1 renderer's output", res.Rendered)
	}
	if len(res.Blockers) != 0 {
		t.Errorf("a passing preflight produced blockers %v", res.Blockers)
	}
}

func TestAFailedPreflightBecomesABlocker(t *testing.T) {
	tgt := &rich{preflightErr: errors.New("rbac: cannot create middlewares")}
	a := v1adapter.New(tgt, meta())

	res, err := a.Plan(context.Background(), targetv2.PlanRequest{Desired: desired("abc")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(res.Blockers) != 1 || !strings.Contains(res.Blockers[0], "rbac") {
		t.Fatalf("blockers are %v, want the preflight failure", res.Blockers)
	}
}

func TestAPlanThatCannotTellAssumesAChange(t *testing.T) {
	// A bare v1 target has no way to know. Reporting "no change" would skip
	// an apply that was needed; reporting a change costs an idempotent apply.
	a := v1adapter.New(&bare{}, meta())

	res, err := a.Plan(context.Background(), targetv2.PlanRequest{Desired: desired("abc")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !res.Changes {
		t.Errorf("a target that cannot diff claimed it would change nothing")
	}
}

func TestObserveCarriesFingerprintAndHealthTogether(t *testing.T) {
	// v1 answers them in two calls; v2 asks once, which is why Health is not
	// a method of its own (ADR-0006).
	tgt := &bare{live: "abc"}
	a := v1adapter.New(tgt, meta())

	obs, err := a.Observe(context.Background(), targetv2.ObserveRequest{})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Fingerprint != "abc" {
		t.Errorf("fingerprint is %q, want abc", obs.Fingerprint)
	}
	if obs.Health.State != targetv2.HealthHealthy || obs.Health.Reason != "all ready" {
		t.Errorf("health is %+v, want the v1 verdict", obs.Health)
	}
}

func TestInspectReportsTheLiveInventory(t *testing.T) {
	a := v1adapter.New(&rich{bare: bare{live: "abc"}}, meta())

	state, err := a.Inspect(context.Background(), targetv2.InspectRequest{})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.Fingerprint != "abc" {
		t.Errorf("fingerprint is %q, want abc", state.Fingerprint)
	}
	if len(state.Resources) != 1 || state.Resources[0].Name != "api" {
		t.Errorf("resources are %+v, want the v1 inspector's", state.Resources)
	}
}

func TestInspectStillWorksWithoutAnInspector(t *testing.T) {
	// Inspect is mandatory in v2. A v1 target that cannot list resources
	// still has a fingerprint, and an empty inventory is the truthful answer
	// rather than a failure.
	a := v1adapter.New(&bare{live: "abc"}, meta())

	state, err := a.Inspect(context.Background(), targetv2.InspectRequest{})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.Fingerprint != "abc" || len(state.Resources) != 0 {
		t.Errorf("state is %+v, want a fingerprint and no resources", state)
	}
}

func TestDriftIsADiffThatIsNotEmpty(t *testing.T) {
	a := v1adapter.New(&rich{diff: "- replicas: 3"}, meta())

	res, err := a.DetectDrift(context.Background(), targetv2.DriftRequest{Desired: desired("abc")})
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}
	if !res.Drifted || res.Detail == "" {
		t.Errorf("drift is %+v, want drifted with detail", res)
	}

	clean := v1adapter.New(&rich{diff: ""}, meta())
	res, err = clean.DetectDrift(context.Background(), targetv2.DriftRequest{Desired: desired("abc")})
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}
	if res.Drifted {
		t.Errorf("an empty diff was reported as drift")
	}
}

func TestPruneReachesTheV1Reaper(t *testing.T) {
	tgt := &rich{}
	a := v1adapter.New(tgt, meta())

	res, err := a.Prune(context.Background(), targetv2.PruneRequest{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if res.Removed != 3 || tgt.reaped != 1 {
		t.Errorf("prune removed %d over %d reaps, want 3 over 1", res.Removed, tgt.reaped)
	}
}

func TestTheAdapterSatisfiesTheV2Contract(t *testing.T) {
	var _ targetv2.Target = v1adapter.New(&bare{}, meta())
	var _ targetv2.Drifter = v1adapter.New(&bare{}, meta())
	var _ targetv2.Pruner = v1adapter.New(&bare{}, meta())
	var _ targetv2.Promoter = v1adapter.New(&bare{}, meta())
}
