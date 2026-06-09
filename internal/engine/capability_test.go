package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	itarget "go.klarlabs.de/rolloffs/internal/target"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

func TestDiff_FromTarget(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: loadConfig(t)})
	out, err := e.Diff(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.HasPrefix(out, "diff for ") {
		t.Errorf("diff = %q", out)
	}
}

func TestResources_FromTarget(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	r, _ := e.Apply(context.Background(), ApplyRequest{Config: loadConfig(t)})
	res, err := e.Resources(context.Background(), r.ID)
	if err != nil {
		t.Fatalf("Resources: %v", err)
	}
	if len(res) != 1 || res[0].Kind != "Deployment" {
		t.Errorf("resources = %+v", res)
	}
}

// engineCounterIDs builds an engine whose ids increment, so multiple rollouts to
// the same target are distinct records.
func engineCounterIDs(t *testing.T, fake *fakeTarget) *Engine {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return fake, nil })
	n := 0
	tick := 0
	return New(db, reg,
		// Incrementing clock so rollouts have distinct, ordered timestamps.
		WithClock(func() time.Time { tick++; return time.Unix(int64(tick), 0) }),
		WithIDGen(func() string { n++; return "ro-" + string(rune('0'+n)) }),
	)
}

func TestRollbackLast_RevertsToPrior(t *testing.T) {
	fake := &fakeTarget{}
	e := engineCounterIDs(t, fake)
	ctx := context.Background()

	c1 := loadConfig(t) // spec x:1
	r1, _ := e.Apply(ctx, ApplyRequest{Config: c1})
	_, _ = e.Promote(ctx, r1.ID)

	c2 := loadConfig(t)
	c2.Spec.Target.Spec = map[string]any{"x": 2} // new checksum
	r2, _ := e.Apply(ctx, ApplyRequest{Config: c2})
	_, _ = e.Promote(ctx, r2.ID)

	if r1.Desired.Checksum == r2.Desired.Checksum {
		t.Fatal("precondition: the two deploys must differ")
	}

	out, err := e.RollbackLast(ctx, "demo/prod/app")
	if err != nil {
		t.Fatalf("RollbackLast: %v", err)
	}
	if string(out.Phase) != "rolled-back" {
		t.Errorf("phase = %q, want rolled-back", out.Phase)
	}
	last := fake.applied[len(fake.applied)-1]
	if last.Checksum != r1.Desired.Checksum {
		t.Errorf("rollback re-applied %q, want the prior manifest %q", last.Checksum, r1.Desired.Checksum)
	}
}

func TestRollbackLast_NoPrior(t *testing.T) {
	e, _ := newEngine(t, &fakeTarget{})
	_, _ = e.Apply(context.Background(), ApplyRequest{Config: loadConfig(t)})
	if _, err := e.RollbackLast(context.Background(), "demo/prod/app"); err == nil {
		t.Fatal("single rollout → no prior state → should error")
	}
}
