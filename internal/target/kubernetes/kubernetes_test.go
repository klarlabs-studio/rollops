package kubernetes

import (
	"context"
	"testing"

	"go.klarlabs.de/rolloffs/pkg/conformance"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// fakeCluster is an in-memory cluster: it records the deployed checksum live.
type fakeCluster struct {
	checksum string
	applied  [][]byte
	unready  bool
}

func (c *fakeCluster) Apply(_ context.Context, manifest []byte, checksum string) error {
	c.applied = append(c.applied, manifest)
	c.checksum = checksum
	return nil
}
func (c *fakeCluster) LiveChecksum(context.Context) (string, error) { return c.checksum, nil }
func (c *fakeCluster) Healthy(context.Context) (bool, string, error) {
	if c.unready {
		return false, "progressing", nil
	}
	return true, "", nil
}
func (c *fakeCluster) Diff(_ context.Context, manifest []byte) (string, error) {
	return "+ " + string(manifest), nil
}
func (c *fakeCluster) Resources(context.Context) ([]pt.Resource, error) {
	return []pt.Resource{{Kind: "Deployment", Name: "web", Namespace: "ns", Status: "ready 2/2"}}, nil
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

func TestTarget_DifferAndInspector(t *testing.T) {
	tgt := newWith(&fakeCluster{})
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
	if err != nil || len(res) != 1 || res[0].Kind != "Deployment" {
		t.Fatalf("Resources: %+v %v", res, err)
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
