package reconcile

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const cfgYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: demo
spec:
  target:
    kind: fake
    ref: demo/prod/app
    criticality: low
    spec:
      x: 1
  strategy:
    type: rolling
`

type fakeTarget struct {
	applied []pt.Manifest
	fp      pt.Fingerprint
}

func (f *fakeTarget) Apply(_ context.Context, m pt.Manifest) (pt.Result, error) {
	f.applied = append(f.applied, m)
	f.fp = pt.Fingerprint{Value: m.Checksum} // deploy stamps the checksum
	return pt.Result{Changed: true}, nil
}
func (f *fakeTarget) Observe(context.Context) (pt.Fingerprint, error) { return f.fp, nil }
func (f *fakeTarget) Health(context.Context) (pt.HealthStatus, error) {
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

func setup(t *testing.T, fake *fakeTarget) (*Reconciler, *bytes.Buffer, *config.Config) {
	t.Helper()
	db, err := sqlite.Open(t.TempDir() + "/r.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	eng := engine.New(db, reg, engine.WithClock(func() time.Time { return clock }), engine.WithIDGen(func() string { return "ro1" }))

	var buf bytes.Buffer
	c, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	return New(eng, audit.New(&buf)), &buf, c
}

var actor = rollout.Identity{Kind: "ci", Name: "reconciler"}

func TestReconcile_DriftDetectedAndReconciled(t *testing.T) {
	fake := &fakeTarget{fp: pt.Fingerprint{Value: "stale"}} // observed != desired
	r, buf, c := setup(t, fake)

	out, err := r.Reconcile(context.Background(), c, actor)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !out.Drift || !out.Reconciled {
		t.Fatalf("expected drift+reconcile, got %+v", out)
	}
	if len(fake.applied) != 1 {
		t.Errorf("drift should trigger one apply, got %d", len(fake.applied))
	}
	if !strings.Contains(buf.String(), `"action":"drift"`) {
		t.Errorf("drift should be audited: %s", buf.String())
	}
}

func TestReconcile_InSyncNoop(t *testing.T) {
	fake := &fakeTarget{}
	r, _, c := setup(t, fake)
	// First reconcile deploys (create); fake now carries the desired checksum.
	if _, err := r.Reconcile(context.Background(), c, actor); err != nil {
		t.Fatal(err)
	}
	applied := len(fake.applied)

	// Second reconcile: observed == desired → no drift, no further apply.
	out, err := r.Reconcile(context.Background(), c, actor)
	if err != nil {
		t.Fatal(err)
	}
	if out.Drift {
		t.Error("in-sync target should report no drift")
	}
	if len(fake.applied) != applied {
		t.Errorf("in-sync reconcile must not re-apply: %d -> %d", applied, len(fake.applied))
	}
}
