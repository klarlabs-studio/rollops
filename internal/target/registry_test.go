package target

import (
	"context"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
)

type noopTarget struct{}

func (noopTarget) Apply(context.Context, pt.Manifest) (pt.Result, error) {
	return pt.Result{}, nil
}
func (noopTarget) Observe(context.Context) (pt.Fingerprint, error) { return pt.Fingerprint{}, nil }
func (noopTarget) Health(context.Context) (pt.HealthStatus, error) { return pt.HealthStatus{}, nil }

func TestRegistry_BuildKnown(t *testing.T) {
	r := NewRegistry()
	r.Register("ssh", func(config.Target) (pt.Target, error) { return noopTarget{}, nil })
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
	r.Register("ssh", func(config.Target) (pt.Target, error) { return noopTarget{}, nil })
	_, err := r.Build(config.Target{Kind: "ftp"})
	if err == nil {
		t.Fatal("expected unknown-kind error")
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate registration")
		}
	}()
	r := NewRegistry()
	f := func(config.Target) (pt.Target, error) { return noopTarget{}, nil }
	r.Register("ssh", f)
	r.Register("ssh", f) // must panic
}
