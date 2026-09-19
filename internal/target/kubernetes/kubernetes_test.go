package kubernetes

import (
	"context"
	"testing"

	conformancev2 "go.klarlabs.de/rollops/pkg/conformance/v2"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// fakeCluster is an in-memory cluster: it records the deployed checksum live.
type fakeCluster struct {
	checksum   string
	key        string
	applied    [][]byte
	unready    bool
	drift      bool   // when true, Diff reports a non-empty diff (live ≠ desired)
	liveYAML   []byte // live object; LiveYAML returns this
	reapN      int
	reapErr    error
	reapCalled bool

	// preflightErr, when set, makes Preflight refuse. preflightN counts calls,
	// so a test can assert the batch asked BEFORE it applied.
	preflightErr error
	preflightN   int
}

func (c *fakeCluster) Apply(_ context.Context, manifest []byte, checksum, key string) error {
	c.applied = append(c.applied, manifest)
	c.checksum = checksum
	c.key = key
	return nil
}
func (c *fakeCluster) Preflight(_ context.Context, _ []byte) error {
	c.preflightN++
	return c.preflightErr
}
func (c *fakeCluster) LiveChecksum(context.Context) (string, error) { return c.checksum, nil }
func (c *fakeCluster) LiveKey(context.Context) (string, error)      { return c.key, nil }
func (c *fakeCluster) LiveYAML(context.Context) ([]byte, error)     { return c.liveYAML, nil }
func (c *fakeCluster) Healthy(context.Context) (bool, string, error) {
	if c.unready {
		return false, "progressing", nil
	}
	return true, "", nil
}
func (c *fakeCluster) Diff(_ context.Context, manifest []byte) (string, error) {
	if c.drift {
		return "+ " + string(manifest), nil
	}
	return "", nil // in sync
}
func (c *fakeCluster) Resources(context.Context) ([]targetv2.Resource, error) {
	return []targetv2.Resource{
		{Kind: "Deployment", Name: "web", Namespace: "ns", Status: "ready 2/2"},
		{Kind: "Pod", Name: "web-abc", Namespace: "ns", Status: "Running · ready", Parent: "web"},
		{Kind: "Pod", Name: "web-def", Namespace: "ns", Status: "Running · ready", Parent: "web"},
	}, nil
}
func (c *fakeCluster) ReapTarget(context.Context) (int, error) {
	c.reapCalled = true
	return c.reapN, c.reapErr
}

var sample = targetv2.DesiredState{
	Kind:     "kubernetes",
	Spec:     []byte("apiVersion: apps/v1\nkind: Deployment\n"),
	Checksum: "sum-k8s-v5",
}

func key(s string) string { return targetv2.IdempotencyKeyFor("kubernetes-test", s) }

// TestConformance measures this target against §9.5's ten axes directly —
// no adapter in between, which is the point of the port.
//
// Invalid is a spec that parses as JSON and names none of the sources this
// target knows how to render, which is the one malformed input it can be
// given without a cluster. Supplying it is what makes the typed-error axis
// reach past the shared idempotency-key guard and into this target's own
// refusals.
func TestConformance(t *testing.T) {
	s := conformancev2.Suite{
		New:     func() (targetv2.Target, error) { return newWith(&fakeCluster{}), nil },
		Desired: sample,
		Invalid: targetv2.DesiredState{
			Kind:     "kubernetes",
			Spec:     []byte(`{"notASource":"anything"}`),
			Checksum: "sum-k8s-invalid",
		},
	}
	s.Run(t)
}

func TestApply_RichObserveIdempotent(t *testing.T) {
	cl := &fakeCluster{}
	tgt := newWith(cl)
	ctx := context.Background()

	r1, _ := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: key("one")})
	if !r1.Changed || len(cl.applied) != 1 {
		t.Fatalf("first apply: changed=%v applied=%d", r1.Changed, len(cl.applied))
	}
	obs, _ := tgt.Observe(ctx, targetv2.ObserveRequest{})
	if obs.Fingerprint != sample.Checksum {
		t.Errorf("observed %q (from live cluster), want %q", obs.Fingerprint, sample.Checksum)
	}
	// A different key over the same desired state is a new operation, not a
	// replay, so this measures convergence rather than the idempotency memo.
	r2, _ := tgt.Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: key("two")})
	if r2.Changed || len(cl.applied) != 1 {
		t.Errorf("re-apply should be no-op: changed=%v applied=%d", r2.Changed, len(cl.applied))
	}
}

// TestApply_ReferencedSource_UsesRenderedBytesWithoutRoot proves the rollback
// fix: when a manifest carries captured Rendered bytes (a referenced
// manifestFrom source deployed earlier), Apply uses them verbatim and never
// re-renders — so a rollback works with no checkout Root, even if the referenced
// source would no longer resolve.
func TestApply_ReferencedSource_UsesRenderedBytesWithoutRoot(t *testing.T) {
	cl := &fakeCluster{}
	tgt := newWith(cl)
	rendered := []byte("apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: api\n")
	d := targetv2.DesiredState{
		Kind:     "kubernetes",
		Spec:     []byte(`{"manifestFrom":{"path":"does/not/exist.yaml"}}`),
		Rendered: rendered,
		Checksum: "sum-rendered",
	}

	res, err := tgt.Apply(context.Background(), targetv2.ApplyRequest{Desired: d, IdempotencyKey: key("rendered")})
	if err != nil {
		t.Fatalf("apply must reuse captured Rendered, not re-render: %v", err)
	}
	if !res.Changed || len(cl.applied) != 1 {
		t.Fatalf("expected one apply, changed=%v applied=%d", res.Changed, len(cl.applied))
	}
	if string(cl.applied[0]) != string(rendered) {
		t.Errorf("applied %q, want the stored rendered bytes %q", cl.applied[0], rendered)
	}
}

// TestApply_ReferencedSource_NoRendered_NoRoot_Errors guards the fallback: the
// same unresolvable referenced source with NO captured Rendered and no Root
// fails to render — proving it is the stored Rendered bytes that make rollback
// root-independent, not a lenient renderer silently applying nothing.
func TestApply_ReferencedSource_NoRendered_NoRoot_Errors(t *testing.T) {
	tgt := newWith(&fakeCluster{})
	d := targetv2.DesiredState{
		Kind: "kubernetes",
		Spec: []byte(`{"manifestFrom":{"path":"does/not/exist.yaml"}}`),
	}
	_, err := tgt.Apply(context.Background(), targetv2.ApplyRequest{Desired: d, IdempotencyKey: key("unresolvable")})
	if err == nil {
		t.Fatal("expected an error rendering an unresolvable referenced source with no captured Rendered")
	}
	// A source that will not resolve is malformed input, not an internal
	// failure: the host retries what is unavailable and does not retry this.
	if got := targetv2.KindOf(err); got != targetv2.KindInvalid {
		t.Errorf("an unrenderable source was reported as kind %q, want %q", got, targetv2.KindInvalid)
	}
}

func TestApply_ReappliesOnDriftDespiteMatchingStamp(t *testing.T) {
	// Stamp already matches desired (out-of-band edit preserved it), but the
	// cluster has drifted — Apply must re-apply to correct it.
	cl := &fakeCluster{checksum: sample.Checksum, drift: true}
	tgt := newWith(cl)
	r, err := tgt.Apply(context.Background(), targetv2.ApplyRequest{Desired: sample, IdempotencyKey: key("drift")})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !r.Changed || len(cl.applied) != 1 {
		t.Fatalf("drift with matching stamp must re-apply: changed=%v applied=%d", r.Changed, len(cl.applied))
	}
}

func TestTarget_DriftAndInspect(t *testing.T) {
	tgt := newWith(&fakeCluster{drift: true})
	ctx := context.Background()

	dr, err := tgt.DetectDrift(ctx, targetv2.DriftRequest{Desired: sample})
	if err != nil || !dr.Drifted || dr.Detail == "" {
		t.Fatalf("DetectDrift: %+v %v", dr, err)
	}

	state, err := tgt.Inspect(ctx, targetv2.InspectRequest{})
	if err != nil || len(state.Resources) < 1 || state.Resources[0].Kind != "Deployment" {
		t.Fatalf("Inspect: %+v %v", state, err)
	}
	// Tree: child pods carry Parent.
	var children int
	for _, r := range state.Resources {
		if r.Parent != "" {
			children++
		}
	}
	if children == 0 {
		t.Error("expected child pods in the resource tree")
	}
}

func TestHealth_RolloutReadiness(t *testing.T) {
	cl := &fakeCluster{}
	tgt := newWith(cl)
	ctx := context.Background()
	if obs, _ := tgt.Observe(ctx, targetv2.ObserveRequest{}); obs.Health.State != targetv2.HealthHealthy {
		t.Error("ready rollout should be healthy")
	}
	cl.unready = true
	if obs, _ := tgt.Observe(ctx, targetv2.ObserveRequest{}); obs.Health.State != targetv2.HealthUnhealthy {
		t.Error("progressing rollout should be unhealthy")
	}
}

// #154: orphan reclamation asks the binding whether the target can prune, so
// Prune must be on Target — not only on kubectlCluster — or reclamation logs
// "target kind kubernetes cannot reap" and leaves the orphan running.
func TestTarget_Prunes(t *testing.T) {
	cl := &fakeCluster{reapN: 3}
	tgt := newWith(cl)
	if !capabilities().Prune {
		t.Fatal("kubernetes must declare prune so orphan reclamation reaches it")
	}
	res, err := tgt.Prune(context.Background(), targetv2.PruneRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 3 || !cl.reapCalled {
		t.Fatalf("Prune forwarded incorrectly: removed=%d called=%v", res.Removed, cl.reapCalled)
	}
}

// TestASpentKeyReplaysRatherThanReapplying is §9.4 from the side that matters:
// the retry comes from a process that crashed between the call and its
// response, so the answer has to be on the cluster rather than in memory.
func TestASpentKeyReplaysRatherThanReapplying(t *testing.T) {
	cl := &fakeCluster{}
	ctx := context.Background()
	k := key("crash")

	first, err := newWith(cl).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// A fresh Target is the retry: the process that made the first call is gone.
	replay, err := newWith(cl).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.Changed != first.Changed {
		t.Errorf("the replay reported changed=%v, want the first answer %v", replay.Changed, first.Changed)
	}
	if len(cl.applied) != 1 {
		t.Errorf("the cluster was applied to %d times, want 1", len(cl.applied))
	}
}

// TestTheSameKeyOverADifferentStateIsAConflict is the other branch: guessing
// which of two requests the caller meant is worse than refusing.
func TestTheSameKeyOverADifferentStateIsAConflict(t *testing.T) {
	cl := &fakeCluster{}
	ctx := context.Background()
	k := key("reused")

	if _, err := newWith(cl).Apply(ctx, targetv2.ApplyRequest{Desired: sample, IdempotencyKey: k}); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	other := sample
	other.Checksum = "sum-k8s-v6"
	_, err := newWith(cl).Apply(ctx, targetv2.ApplyRequest{Desired: other, IdempotencyKey: k})
	if got := targetv2.KindOf(err); got != targetv2.KindConflict {
		t.Errorf("a reused key over a different desired state was reported as kind %q, want %q", got, targetv2.KindConflict)
	}
	if len(cl.applied) != 1 {
		t.Errorf("the conflicting apply reached the cluster: applied %d times, want 1", len(cl.applied))
	}
}
