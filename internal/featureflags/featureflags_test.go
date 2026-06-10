package featureflags

import (
	"context"
	"testing"
)

type fakeProvider struct{ got Change }

func (f *fakeProvider) ApplyFlag(_ context.Context, c Change) error {
	f.got = c
	return nil
}

func TestHookApply(t *testing.T) {
	p := &fakeProvider{}
	h := Hook{Provider: p}
	in := Change{Flag: "checkout", Environment: "prod", Percentage: 25}
	if err := h.Apply(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if p.got != in {
		t.Fatalf("got %+v want %+v", p.got, in)
	}
}

func TestHookApplyValidation(t *testing.T) {
	if err := (Hook{Provider: &fakeProvider{}}).Apply(context.Background(), Change{Percentage: 101}); err == nil {
		t.Fatal("invalid flag change should fail")
	}
	if err := (Hook{}).Apply(context.Background(), Change{}); err != nil {
		t.Fatalf("nil provider should be inert: %v", err)
	}
}
