package reconcile

import (
	"testing"

	"go.klarlabs.de/rollops/internal/config"
)

func TestConfigsForPreflightGate_SkipsContinueOnFailure(t *testing.T) {
	a := &config.Config{}
	a.Spec.Target.Ref = "a"
	b := &config.Config{}
	b.Spec.Target.Ref = "b"
	b.Spec.ContinueOnFailure = true
	c := &config.Config{}
	c.Spec.Target.Ref = "c"

	got := configsForPreflightGate([]*config.Config{a, b, c})
	if len(got) != 2 {
		t.Fatalf("gate size = %d, want 2 (b excluded)", len(got))
	}
	if got[0].Spec.Target.Ref != "a" || got[1].Spec.Target.Ref != "c" {
		t.Fatalf("gate refs = [%s %s], want [a c]", got[0].Spec.Target.Ref, got[1].Spec.Target.Ref)
	}
}

func TestConfigsForPreflightGate_AllContinueLeavesEmptyGate(t *testing.T) {
	a := &config.Config{}
	a.Spec.ContinueOnFailure = true
	got := configsForPreflightGate([]*config.Config{a})
	if len(got) != 0 {
		t.Fatalf("gate size = %d, want 0", len(got))
	}
}

func TestConfigsForPreflightGate_NilSafe(t *testing.T) {
	if got := configsForPreflightGate(nil); len(got) != 0 {
		t.Fatalf("nil in → %v", got)
	}
}
