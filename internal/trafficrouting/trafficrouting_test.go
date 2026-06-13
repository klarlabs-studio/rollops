package trafficrouting

import (
	"context"
	"errors"
	"testing"
)

type fakeRouter struct {
	got  Change
	fail error
}

func (f *fakeRouter) SetWeight(_ context.Context, c Change) error {
	f.got = c
	return f.fail
}

func TestHook_NilRouterIsNoop(t *testing.T) {
	if err := (Hook{}).Apply(context.Background(), Change{Route: "r", Weight: 50}); err != nil {
		t.Fatalf("nil router must be a no-op, got %v", err)
	}
}

func TestHook_ValidatesRouteAndWeight(t *testing.T) {
	h := Hook{Router: &fakeRouter{}}
	if err := h.Apply(context.Background(), Change{Weight: 50}); err == nil {
		t.Error("missing route must error")
	}
	if err := h.Apply(context.Background(), Change{Route: "r", Weight: 101}); err == nil {
		t.Error("weight > 100 must error")
	}
	if err := h.Apply(context.Background(), Change{Route: "r", Weight: -1}); err == nil {
		t.Error("weight < 0 must error")
	}
}

func TestHook_RoutesValidChange(t *testing.T) {
	fr := &fakeRouter{}
	h := Hook{Router: fr}
	if err := h.Apply(context.Background(), Change{Route: "r", CanaryService: "c", StableService: "s", Weight: 30}); err != nil {
		t.Fatalf("valid change: %v", err)
	}
	if fr.got.Weight != 30 || fr.got.Route != "r" || fr.got.CanaryService != "c" {
		t.Errorf("router got %+v", fr.got)
	}
}

func TestHook_PropagatesRouterError(t *testing.T) {
	h := Hook{Router: &fakeRouter{fail: errors.New("boom")}}
	if err := h.Apply(context.Background(), Change{Route: "r", Weight: 10}); err == nil {
		t.Fatal("router error must propagate")
	}
}
