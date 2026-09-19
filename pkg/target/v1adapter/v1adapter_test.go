package v1adapter_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	last    v1.Manifest
}

func (b *bare) Apply(_ context.Context, m v1.Manifest) (v1.Result, error) {
	b.applies++
	b.last = m
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
	inline       bool // the spec is the manifest, so its checksum already identifies it
}

func (r *rich) Diff(context.Context, v1.Manifest) (string, error) { return r.diff, nil }

func (r *rich) Preflight(context.Context, v1.Manifest) error { return r.preflightErr }

func (r *rich) Render(_ context.Context, m v1.Manifest) ([]byte, error) {
	return append([]byte("rendered:"), m.Spec...), nil
}

func (r *rich) Referenced(v1.Manifest) bool { return !r.inline }

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

// closingBare is a v1 target that holds a resource, the way a plugin-backed one
// holds a subprocess.
type closingBare struct {
	bare
	closed int
}

func (c *closingBare) Close() error { c.closed++; return nil }

func TestTheAdapterClosesThroughToTheV1TargetExactlyOnce(t *testing.T) {
	// The binding looks for a closer and finds the adapter, not the target
	// behind it, so an adapter that does not forward leaks whatever the target
	// held — silently, because a leaked resource still answers. Forwarding
	// twice is the opposite failure and just as real: the second close of a
	// plugin subprocess kills a pid the kernel may have handed to somebody else.
	inner := &closingBare{}
	a := v1adapter.New(inner, meta())

	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if inner.closed != 1 {
		t.Errorf("the v1 target was closed %d times, want 1", inner.closed)
	}

	// A v1 target with nothing to release is the common case and must not be an
	// error: the caller closes unconditionally and cannot tell the two apart.
	if err := v1adapter.New(&bare{}, meta()).Close(); err != nil {
		t.Errorf("closing a target that holds nothing failed: %v", err)
	}
}

func TestResolvedBytesReachTheV1Target(t *testing.T) {
	// A spec that points somewhere is checksummed over the pointer, and the
	// bytes it resolved to are the only thing a rollback can restore without
	// resolving it again from a checkout it may not have. An adapter that drops
	// them turns a rollback into a fresh render of whatever the source says now.
	tgt := &bare{}
	a := v1adapter.New(tgt, meta())
	d := desired("abc")
	d.Rendered = []byte("kind: Deployment\n")

	if _, err := a.Apply(context.Background(), targetv2.ApplyRequest{Desired: d, IdempotencyKey: "dep-1/op-1"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if string(tgt.last.Rendered) != string(d.Rendered) {
		t.Errorf("the v1 target was handed Rendered %q, want %q", tgt.last.Rendered, d.Rendered)
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

// TestAPointerSpecGetsAChecksumOverWhatItRenderedTo is how drift stays
// measurable against a referenced source. A spec that names a Helm chart or a
// path has a checksum over the pointer, and editing the files behind it leaves
// that checksum untouched — so the plan carries a second one, taken over the
// bytes the pointer actually resolved to.
func TestAPointerSpecGetsAChecksumOverWhatItRenderedTo(t *testing.T) {
	a := v1adapter.New(&rich{}, meta())
	res, err := a.Plan(context.Background(), targetv2.PlanRequest{Desired: desired("abc")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	sum := sha256.Sum256(res.Rendered)
	if want := hex.EncodeToString(sum[:]); res.RenderedChecksum != want {
		t.Errorf("rendered checksum %q, want %q — the sum of what Plan rendered", res.RenderedChecksum, want)
	}
}

// TestAnInlineSpecKeepsItsOwnChecksum is the other half. The spec is the
// manifest, so its checksum already identifies what gets applied and a second
// one would be the engine re-keying a manifest that never moved.
func TestAnInlineSpecKeepsItsOwnChecksum(t *testing.T) {
	a := v1adapter.New(&rich{inline: true}, meta())
	res, err := a.Plan(context.Background(), targetv2.PlanRequest{Desired: desired("abc")})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res.RenderedChecksum != "" {
		t.Errorf("an inline spec was re-keyed to %q", res.RenderedChecksum)
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

// TestADeadContextStopsAtTheAdapter covers the v1 targets that never learned
// to check one. bare ignores its context entirely — which is legal in v1 and
// not in v2 — so if the refusal did not happen here it would not happen.
func TestADeadContextStopsAtTheAdapter(t *testing.T) {
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	tgt := &rich{}
	a := v1adapter.New(tgt, meta())

	calls := map[string]func(context.Context) error{
		"Inspect": func(ctx context.Context) error {
			_, err := a.Inspect(ctx, targetv2.InspectRequest{})
			return err
		},
		"Plan": func(ctx context.Context) error {
			_, err := a.Plan(ctx, targetv2.PlanRequest{Desired: desired("abc")})
			return err
		},
		"Apply": func(ctx context.Context) error {
			_, err := a.Apply(ctx, targetv2.ApplyRequest{Desired: desired("abc"), IdempotencyKey: "k"})
			return err
		},
		"Observe": func(ctx context.Context) error {
			_, err := a.Observe(ctx, targetv2.ObserveRequest{})
			return err
		},
		"DetectDrift": func(ctx context.Context) error {
			_, err := a.DetectDrift(ctx, targetv2.DriftRequest{Desired: desired("abc")})
			return err
		},
		"Prune": func(ctx context.Context) error {
			_, err := a.Prune(ctx, targetv2.PruneRequest{})
			return err
		},
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call(cancelled())
			if err == nil {
				t.Fatalf("%s ran on a cancelled context", name)
			}
			if got := targetv2.KindOf(err); got != targetv2.KindCanceled {
				t.Errorf("%s failed with kind %q, want %q", name, got, targetv2.KindCanceled)
			}
		})
	}

	if tgt.applies != 0 || tgt.reaped != 0 {
		t.Errorf("the v1 target was reached anyway: %d applies, %d reaps", tgt.applies, tgt.reaped)
	}
}

func TestTheAdapterSatisfiesTheV2Contract(t *testing.T) {
	var _ targetv2.Target = v1adapter.New(&bare{}, meta())
	var _ targetv2.Drifter = v1adapter.New(&bare{}, meta())
	var _ targetv2.Pruner = v1adapter.New(&bare{}, meta())
	var _ targetv2.Promoter = v1adapter.New(&bare{}, meta())
}
