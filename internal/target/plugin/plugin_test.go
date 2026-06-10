package plugin

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/rollops/pkg/conformance"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// fakeRPC is an in-memory stamped plugin backend.
type fakeRPC struct{ current string }

func (f *fakeRPC) Apply(_ context.Context, _ string, _ []byte, checksum string) (bool, string, error) {
	changed := f.current != checksum
	f.current = checksum
	return changed, "applied", nil
}
func (f *fakeRPC) Observe(context.Context) (string, map[string]string, error) {
	return f.current, nil, nil
}
func (f *fakeRPC) Health(context.Context) (int, string, error) {
	return int(pt.HealthHealthy), "", nil
}

func TestVerifyHandshake(t *testing.T) {
	if err := VerifyHandshake(Handshake{ProtocolVersion: ProtocolVersion, Cookie: Cookie}); err != nil {
		t.Errorf("valid handshake rejected: %v", err)
	}
	if err := VerifyHandshake(Handshake{ProtocolVersion: 999, Cookie: Cookie}); err == nil {
		t.Error("version mismatch must be rejected")
	}
	if err := VerifyHandshake(Handshake{ProtocolVersion: ProtocolVersion, Cookie: "nope"}); err == nil {
		t.Error("bad cookie must be rejected")
	}
}

func TestPluginTarget_Conformance(t *testing.T) {
	conformance.Run(t, func() (pt.Target, error) {
		return NewTarget(&fakeRPC{}), nil
	}, pt.Manifest{Kind: "exotic", Spec: []byte("x"), Checksum: "sum-plugin"})
}

func TestPluginTarget_ForwardsHealthState(t *testing.T) {
	tgt := NewTarget(&fakeRPC{})
	hs, err := tgt.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hs.State != pt.HealthHealthy {
		t.Errorf("health state = %v, want healthy", hs.State)
	}
}

func TestPluginTarget_NilRPCReturnsError(t *testing.T) {
	tgt := NewTarget(nil)
	if _, err := tgt.Apply(context.Background(), pt.Manifest{}); !errors.Is(err, ErrNoRPC) {
		t.Fatalf("Apply error = %v, want ErrNoRPC", err)
	}
	if _, err := tgt.Observe(context.Background()); !errors.Is(err, ErrNoRPC) {
		t.Fatalf("Observe error = %v, want ErrNoRPC", err)
	}
	if _, err := tgt.Health(context.Background()); !errors.Is(err, ErrNoRPC) {
		t.Fatalf("Health error = %v, want ErrNoRPC", err)
	}
}

type badHealthRPC struct {
	fakeRPC
	state int
}

func (b badHealthRPC) Health(context.Context) (int, string, error) {
	return b.state, "", nil
}

func TestPluginTarget_RejectsInvalidHealthStates(t *testing.T) {
	for _, state := range []int{int(pt.HealthUnknown), -1, 99} {
		t.Run("state", func(t *testing.T) {
			tgt := NewTarget(&badHealthRPC{state: state})
			if _, err := tgt.Health(context.Background()); err == nil {
				t.Fatalf("expected invalid health state %d to be rejected", state)
			}
		})
	}
}
