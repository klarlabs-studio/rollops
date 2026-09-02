package kubernetes

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/pkg/conformance"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// fakeCluster is an in-memory cluster: it records the deployed checksum live.
type fakeCluster struct {
	checksum   string
	applied    [][]byte
	unready    bool
	drift      bool   // when true, Diff reports a non-empty diff (live ≠ desired)
	liveYAML   []byte // live object; LiveYAML returns this
	reapN      int
	reapErr    error
	reapCalled bool
}

func (c *fakeCluster) Apply(_ context.Context, manifest []byte, checksum string) error {
	c.applied = append(c.applied, manifest)
	c.checksum = checksum
	return nil
}
func (c *fakeCluster) LiveChecksum(context.Context) (string, error) { return c.checksum, nil }
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
func (c *fakeCluster) Resources(context.Context) ([]pt.Resource, error) {
	return []pt.Resource{
		{Kind: "Deployment", Name: "web", Namespace: "ns", Status: "ready 2/2"},
		{Kind: "Pod", Name: "web-abc", Namespace: "ns", Status: "Running · ready", Parent: "web"},
		{Kind: "Pod", Name: "web-def", Namespace: "ns", Status: "Running · ready", Parent: "web"},
	}, nil
}
func (c *fakeCluster) ReapTarget(context.Context) (int, error) {
	c.reapCalled = true
	return c.reapN, c.reapErr
}

var sample = pt.Manifest{Kind: "kubernetes", Spec: []byte("apiVersion: apps/v1\nkind: Deployment\n"), Checksum: "sum-k8s-v5"}

func TestConformance(t *testing.T) {
	conformance.Run(t, func() (pt.Target, error) {
		return newWith(&fakeCluster{}), nil
	}, sample)
}

func TestApply_RichObserveIdempotent(t *testing.T) {
	cl := &fakeCluster{}
	tgt := newWith(cl)
	ctx := context.Background()

	r1, _ := tgt.Apply(ctx, sample)
	if !r1.Changed || len(cl.applied) != 1 {
		t.Fatalf("first apply: changed=%v applied=%d", r1.Changed, len(cl.applied))
	}
	fp, _ := tgt.Observe(ctx)
	if fp.Value != sample.Checksum {
		t.Errorf("observed %q (from live cluster), want %q", fp.Value, sample.Checksum)
	}
	r2, _ := tgt.Apply(ctx, sample)
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
	m := pt.Manifest{
		Kind:     "kubernetes",
		Spec:     []byte(`{"manifestFrom":{"path":"does/not/exist.yaml"}}`),
		Root:     "", // no checkout — as on a manual CLI/UI/API rollback
		Rendered: rendered,
		Checksum: "sum-rendered",
	}

	res, err := tgt.Apply(context.Background(), m)
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
	m := pt.Manifest{
		Kind: "kubernetes",
		Spec: []byte(`{"manifestFrom":{"path":"does/not/exist.yaml"}}`),
		Root: "",
	}
	if _, err := tgt.Apply(context.Background(), m); err == nil {
		t.Fatal("expected an error rendering an unresolvable referenced source with no captured Rendered")
	}
}

func TestApply_ReappliesOnDriftDespiteMatchingStamp(t *testing.T) {
	// Stamp already matches desired (out-of-band edit preserved it), but the
	// cluster has drifted — Apply must re-apply to correct it.
	cl := &fakeCluster{checksum: sample.Checksum, drift: true}
	tgt := newWith(cl)
	r, err := tgt.Apply(context.Background(), sample)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !r.Changed || len(cl.applied) != 1 {
		t.Fatalf("drift with matching stamp must re-apply: changed=%v applied=%d", r.Changed, len(cl.applied))
	}
}

func TestTarget_DifferAndInspector(t *testing.T) {
	tgt := newWith(&fakeCluster{drift: true})
	ctx := context.Background()

	d, ok := pt.Target(tgt).(pt.Differ)
	if !ok {
		t.Fatal("k8s target should implement Differ")
	}
	out, err := d.Diff(ctx, sample)
	if err != nil || out == "" {
		t.Fatalf("Diff: %q %v", out, err)
	}

	insp, ok := pt.Target(tgt).(pt.Inspector)
	if !ok {
		t.Fatal("k8s target should implement Inspector")
	}
	res, err := insp.Resources(ctx)
	if err != nil || len(res) < 1 || res[0].Kind != "Deployment" {
		t.Fatalf("Resources: %+v %v", res, err)
	}
	// Tree: child pods carry Parent.
	var children int
	for _, r := range res {
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
	if hs, _ := tgt.Health(context.Background()); hs.State != pt.HealthHealthy {
		t.Error("ready rollout should be healthy")
	}
	cl.unready = true
	if hs, _ := tgt.Health(context.Background()); hs.State != pt.HealthUnhealthy {
		t.Error("progressing rollout should be unhealthy")
	}
}

// #154: BuildTarget returns *Target; the orphan reaper type-asserts pt.Reaper.
// ReapTarget must be on Target (not only kubectlCluster), or reclamation logs
// "target kind kubernetes cannot reap" and leaves the orphan running.
func TestTarget_ImplementsReaper(t *testing.T) {
	cl := &fakeCluster{reapN: 3}
	tgt := newWith(cl)
	r, ok := pt.Target(tgt).(pt.Reaper)
	if !ok {
		t.Fatal("kubernetes.Target must implement pt.Reaper so orphan reclamation works")
	}
	n, err := r.ReapTarget(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || !cl.reapCalled {
		t.Fatalf("ReapTarget forwarded incorrectly: n=%d called=%v", n, cl.reapCalled)
	}
}
