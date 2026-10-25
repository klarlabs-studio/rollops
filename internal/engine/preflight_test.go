package engine

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	itarget "go.klarlabs.de/rollops/internal/target"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// preflightFake is a Target that also implements Preflighter. Engine.Preflight
// must reach it through the fortify wrapper — if it type-asserts the Guarded
// target instead of the inner one, the capability is silently skipped and the
// batch gate never fires (#185 / #182).
type preflightFake struct {
	fakeTarget
	n   int
	err error
}

func (f *preflightFake) Preflight(context.Context, pt.Manifest) error {
	f.n++
	return f.err
}

func engineWithTarget(t *testing.T, tgt pt.Target) *Engine {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := itarget.NewRegistry()
	reg.Register("fake", func(config.Target) (pt.Target, error) { return tgt, nil })
	clock := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	return New(db, reg, WithClock(func() time.Time { return clock }), WithIDGen(func() string { return "ro-test" }))
}

// Engine.Preflight must call Preflight on a Preflighter even though build()
// wraps the target in step.Guarded (which does not implement the capability).
func TestPreflight_ReachesThroughFortifyWrapper(t *testing.T) {
	fake := &preflightFake{err: errors.New(`Forbidden: middlewares.traefik.io "x" is forbidden`)}
	e := engineWithTarget(t, fake)

	errs := e.Preflight(context.Background(), []*config.Config{loadConfig(t)})
	if fake.n != 1 {
		t.Fatalf("Preflight called %d times, want 1 — capability was dropped by the fortify wrapper", fake.n)
	}
	if len(errs) != 1 {
		t.Fatalf("errs = %d, want 1 refusal", len(errs))
	}
	if !strings.Contains(errs[0].Error(), "Forbidden") {
		t.Errorf("error = %v, want Forbidden named", errs[0])
	}
	if len(fake.applied) != 0 {
		t.Errorf("Apply ran %d times during Preflight; must change nothing", len(fake.applied))
	}
}

func TestPreflight_NoObjectionWhenClear(t *testing.T) {
	fake := &preflightFake{}
	e := engineWithTarget(t, fake)

	errs := e.Preflight(context.Background(), []*config.Config{loadConfig(t)})
	if fake.n != 1 {
		t.Fatalf("Preflight called %d times, want 1", fake.n)
	}
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
}

func TestPreflight_SkipsNonPreflighter(t *testing.T) {
	// A plain fakeTarget has no Preflight method — Preflight must skip it
	// rather than invent a refusal (that was the pre-#185 behaviour).
	e := engineWithTarget(t, &fakeTarget{})

	errs := e.Preflight(context.Background(), []*config.Config{loadConfig(t)})
	if len(errs) != 0 {
		t.Fatalf("errs = %v, want none for a non-Preflighter", errs)
	}
}
