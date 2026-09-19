package v1adapter_test

import (
	"context"
	"errors"
	"testing"

	v1 "go.klarlabs.de/rollops/pkg/target"
	"go.klarlabs.de/rollops/pkg/target/v1adapter"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// denied is the failure a v1 target returns when it already speaks v2's
// vocabulary — the case §9.6 asks a v1 target to write itself into, because it
// is the only one where the kind is known to anybody.
var denied = targetv2.Failf(targetv2.KindDenied, "v1", nil, "the credentials are not allowed to do that")

// failing fails in exactly one v1 method and succeeds in the rest, so that a
// verb the adapter assembles from several — Inspect from Observe and
// Resources, Plan from Diff and Render — is measured at each of its parts
// rather than only at the first one that can go wrong.
type failing struct {
	rich
	at  string
	err error
}

func (f *failing) at_(name string) error {
	if f.at != name {
		return nil
	}
	return f.err
}

func (f *failing) Apply(ctx context.Context, m v1.Manifest) (v1.Result, error) {
	if err := f.at_("Apply"); err != nil {
		return v1.Result{}, err
	}
	return f.rich.Apply(ctx, m)
}

func (f *failing) Observe(ctx context.Context) (v1.Fingerprint, error) {
	if err := f.at_("Observe"); err != nil {
		return v1.Fingerprint{}, err
	}
	return f.rich.Observe(ctx)
}

func (f *failing) Health(ctx context.Context) (v1.HealthStatus, error) {
	if err := f.at_("Health"); err != nil {
		return v1.HealthStatus{}, err
	}
	return f.rich.Health(ctx)
}

func (f *failing) Diff(ctx context.Context, m v1.Manifest) (string, error) {
	if err := f.at_("Diff"); err != nil {
		return "", err
	}
	return f.rich.Diff(ctx, m)
}

func (f *failing) Render(ctx context.Context, m v1.Manifest) ([]byte, error) {
	if err := f.at_("Render"); err != nil {
		return nil, err
	}
	return f.rich.Render(ctx, m)
}

func (f *failing) Resources(ctx context.Context) ([]v1.Resource, error) {
	if err := f.at_("Resources"); err != nil {
		return nil, err
	}
	return f.rich.Resources(ctx)
}

func (f *failing) ReapTarget(ctx context.Context) (int, error) {
	if err := f.at_("ReapTarget"); err != nil {
		return 0, err
	}
	return f.rich.ReapTarget(ctx)
}

// call names one v2 verb and how to reach it, so the same set can be driven
// once per underlying failure.
type call struct {
	op   string
	call func(context.Context, *v1adapter.Adapter) error
}

func v2Calls() []call {
	return []call{
		{"Inspect", func(ctx context.Context, a *v1adapter.Adapter) error {
			_, err := a.Inspect(ctx, targetv2.InspectRequest{})
			return err
		}},
		{"Plan", func(ctx context.Context, a *v1adapter.Adapter) error {
			_, err := a.Plan(ctx, targetv2.PlanRequest{Desired: desired("sha256:one")})
			return err
		}},
		{"Apply", func(ctx context.Context, a *v1adapter.Adapter) error {
			_, err := a.Apply(ctx, targetv2.ApplyRequest{Desired: desired("sha256:one"), IdempotencyKey: "k"})
			return err
		}},
		{"Observe", func(ctx context.Context, a *v1adapter.Adapter) error {
			_, err := a.Observe(ctx, targetv2.ObserveRequest{})
			return err
		}},
		{"DetectDrift", func(ctx context.Context, a *v1adapter.Adapter) error {
			_, err := a.DetectDrift(ctx, targetv2.DriftRequest{Desired: desired("sha256:one")})
			return err
		}},
		{"Prune", func(ctx context.Context, a *v1adapter.Adapter) error {
			_, err := a.Prune(ctx, targetv2.PruneRequest{})
			return err
		}},
	}
}

// TestAFailureKeepsTheKindItsCauseCarried is what makes the adapter safe to
// leave in front of a target the host branches on. A caller retries an
// unavailable target and must not retry a denied one; flattening both to
// internal is the shape of failure that looks handled and is not.
func TestAFailureKeepsTheKindItsCauseCarried(t *testing.T) {
	for _, at := range []string{"Apply", "Observe", "Health", "Diff", "Render", "Resources", "ReapTarget"} {
		t.Run("v1 "+at+" is denied", func(t *testing.T) {
			var reached int
			for _, c := range v2Calls() {
				a := v1adapter.New(&failing{at: at, err: denied}, meta())
				err := c.call(context.Background(), a)
				if err == nil {
					continue
				}
				reached++
				if got := targetv2.KindOf(err); got != targetv2.KindDenied {
					t.Errorf("%s reported a denial from v1 %s as kind %q, want %q", c.op, at, got, targetv2.KindDenied)
				}
				if !errors.Is(err, denied) {
					t.Errorf("%s did not keep the cause of the v1 %s failure", c.op, at)
				}
			}
			if reached == 0 {
				t.Fatalf("no v2 verb reached v1 %s, so nothing was measured", at)
			}
		})
	}
}

// TestAnUntypedV1FailureIsInternal is the other half, and it is a choice
// rather than an accident: the adapter cannot classify an error it was handed
// no vocabulary for, and a kind callers branch on is not something to put
// behind a heuristic over message text.
func TestAnUntypedV1FailureIsInternal(t *testing.T) {
	bare := errors.New("something went wrong")
	a := v1adapter.New(&failing{at: "Observe", err: bare}, meta())

	_, err := a.Observe(context.Background(), targetv2.ObserveRequest{})
	if got := targetv2.KindOf(err); got != targetv2.KindInternal {
		t.Errorf("a plain v1 error arrived as kind %q, want %q", got, targetv2.KindInternal)
	}
	if !errors.Is(err, bare) {
		t.Errorf("the plain v1 error was not kept as the cause")
	}
}
