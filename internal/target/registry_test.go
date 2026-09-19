package target

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

type noopTarget struct{}

func (noopTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	return pt.Result{}, nil
}
func (noopTarget) Observe(context.Context) (pt.Fingerprint, error) { return pt.Fingerprint{}, nil }
func (noopTarget) Health(context.Context) (pt.HealthStatus, error) { return pt.HealthStatus{}, nil }

func TestRegistry_BuildKnown(t *testing.T) {
	r := NewRegistry()
	r.Register("ssh", FromV1("ssh", func(config.Target) (pt.Target, error) { return noopTarget{}, nil }))
	got, err := r.Build(config.Target{Kind: "ssh", Ref: "x"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got == nil {
		t.Fatal("nil target")
	}
}

func TestRegistry_BuildUnknown(t *testing.T) {
	r := NewRegistry()
	r.Register("ssh", FromV1("ssh", func(config.Target) (pt.Target, error) { return noopTarget{}, nil }))
	_, err := r.Build(config.Target{Kind: "ftp"})
	if err == nil {
		t.Fatal("expected unknown-kind error")
	}
}

// diffingTarget is a v1 target that implements one optional interface.
type diffingTarget struct{ noopTarget }

func (diffingTarget) Diff(context.Context, pt.Manifest) (string, error) { return "", nil }

func TestRegistry_BuildResolvesCapabilitiesFromWhatTheV1TargetImplements(t *testing.T) {
	// The engine reads capabilities off the binding rather than asserting
	// interfaces on it, so a v1 target whose optional interface is never
	// translated into a capability is a target the engine can no longer reach.
	r := NewRegistry()
	r.Register("ssh", FromV1("ssh", func(config.Target) (pt.Target, error) { return diffingTarget{}, nil }))

	got, err := r.Build(config.Target{Kind: "ssh", Ref: "x/prod/one"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !got.Can(targetv2.CapabilityDriftDetection) {
		t.Error("a v1 Differ did not arrive as drift detection")
	}
	if got.Can(targetv2.CapabilityPrune) {
		t.Error("prune was granted to a target that cannot reap")
	}
	if meta := got.Metadata(); meta.Kind != "ssh" || meta.Name != "x/prod/one" {
		t.Errorf("metadata is %+v, want the kind and ref it was built from", meta)
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r := NewRegistry()
	f := FromV1("ssh", func(config.Target) (pt.Target, error) { return noopTarget{}, nil })
	r.Register("ssh", f)
	r.Register("ssh", f) // must panic
}
