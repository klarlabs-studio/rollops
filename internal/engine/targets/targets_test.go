package targets_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/engine/planner"
	"go.klarlabs.de/rollops/internal/engine/targets"
	itarget "go.klarlabs.de/rollops/internal/target"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// stubTarget is the least a registry factory can hand back.
type stubTarget struct{}

func (stubTarget) Metadata() targetv2.Metadata { return targetv2.Metadata{Kind: "fake"} }

func (stubTarget) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return targetv2.Capabilities{}, nil
}

func (stubTarget) Inspect(context.Context, targetv2.InspectRequest) (targetv2.ObservedState, error) {
	return targetv2.ObservedState{}, nil
}

func (stubTarget) Plan(context.Context, targetv2.PlanRequest) (targetv2.PlanResult, error) {
	return targetv2.PlanResult{}, nil
}

func (stubTarget) Apply(context.Context, targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	return targetv2.ApplyResult{}, nil
}

func (stubTarget) Observe(context.Context, targetv2.ObserveRequest) (targetv2.Observation, error) {
	return targetv2.Observation{}, nil
}

func (stubTarget) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return targetv2.RollbackResult{}, nil
}

// secrets answers from a map and reports anything else as missing.
type secrets map[string]string

func (s secrets) ResolveSecret(name string) (string, error) {
	v, ok := s[name]
	if !ok {
		return "", errors.New("no such secret")
	}
	return v, nil
}

// registry returns a registry whose "fake" driver records the config it was
// built from, so a test can see what crossed the bridge.
func registry(built *config.Target) *itarget.Registry {
	r := itarget.NewRegistry()
	r.Register("fake", func(c config.Target) (*targetv2.Bound, error) {
		if built != nil {
			*built = c
		}
		return targetv2.NewBound(stubTarget{}, targetv2.Capabilities{}), nil
	})
	return r
}

func resolver(t *testing.T, r *itarget.Registry, s value.Resolver) *targets.Resolver {
	t.Helper()
	got, err := targets.New(r, s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return got
}

func env(kind environment.Kind) environment.Environment {
	return environment.Environment{
		ID: "env-1", ProjectID: "proj-1", Name: "production", Kind: kind,
	}
}

func binding(cfg map[string]value.Ref) environment.TargetBinding {
	return environment.TargetBinding{Name: "api", Driver: "fake", Config: cfg}
}

func request(b environment.TargetBinding) planner.TargetRequest {
	return planner.TargetRequest{Environment: env(environment.KindProduction), Binding: b}
}

func TestABindingIsBuiltByTheDriverItNames(t *testing.T) {
	var built config.Target
	r := resolver(t, registry(&built), nil)

	bound, err := r.Resolve(context.Background(), request(binding(nil)))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if bound == nil {
		t.Fatal("no target")
	}
	if built.Kind != "fake" {
		t.Errorf("driver %q, want fake", built.Kind)
	}
}

func TestAnUnknownDriverNamesTheBindingThatAskedForIt(t *testing.T) {
	r := resolver(t, itarget.NewRegistry(), nil)

	_, err := r.Resolve(context.Background(), request(binding(nil)))

	if err == nil {
		t.Fatal("an unknown driver was built anyway")
	}
	for _, want := range []string{"api", "fake"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestTheTargetIsNamedByItsEnvironmentAndBinding(t *testing.T) {
	var built config.Target
	r := resolver(t, registry(&built), nil)

	if _, err := r.Resolve(context.Background(), request(binding(nil))); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if want := "production/api"; built.Ref != want {
		t.Errorf("ref %q, want %q", built.Ref, want)
	}
	if want := string(environment.KindProduction); built.Env != want {
		t.Errorf("env %q, want %q", built.Env, want)
	}
}

func TestTwoBindingsInOneEnvironmentAreDifferentTargets(t *testing.T) {
	var built config.Target
	r := resolver(t, registry(&built), nil)
	req := request(binding(nil))

	if _, err := r.Resolve(context.Background(), req); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	first := built.Ref

	req.Binding.Name = "worker"
	if _, err := r.Resolve(context.Background(), req); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if built.Ref == first {
		t.Fatalf("both bindings resolved to %q", first)
	}
}

func TestLiteralConfigurationReachesTheTarget(t *testing.T) {
	var built config.Target
	r := resolver(t, registry(&built), nil)

	_, err := r.Resolve(context.Background(), request(binding(map[string]value.Ref{
		"namespace": value.Literal("payments"),
	})))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := built.Spec["namespace"]; got != "payments" {
		t.Errorf("namespace %v, want payments", got)
	}
}

func TestASecretIsResolvedBeforeTheTargetIsBuilt(t *testing.T) {
	var built config.Target
	r := resolver(t, registry(&built), secrets{"kubeconfig": "the-real-thing"})

	_, err := r.Resolve(context.Background(), request(binding(map[string]value.Ref{
		"kubeconfig": value.Secret("kubeconfig"),
	})))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if got := built.Spec["kubeconfig"]; got != "the-real-thing" {
		t.Errorf("kubeconfig %v, want the resolved secret", got)
	}
}

func TestASecretThatCannotBeResolvedNamesTheKeyAndNotTheValue(t *testing.T) {
	r := resolver(t, registry(nil), secrets{})

	_, err := r.Resolve(context.Background(), request(binding(map[string]value.Ref{
		"kubeconfig": value.Secret("prod-kubeconfig"),
	})))

	if err == nil {
		t.Fatal("an unresolvable secret was passed over")
	}
	for _, want := range []string{"api", "kubeconfig"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestASecretWithNoResolverIsRefused(t *testing.T) {
	r := resolver(t, registry(nil), nil)

	_, err := r.Resolve(context.Background(), request(binding(map[string]value.Ref{
		"kubeconfig": value.Secret("prod-kubeconfig"),
	})))

	if !errors.Is(err, value.ErrNoResolver) {
		t.Fatalf("err %v, want ErrNoResolver", err)
	}
}

func TestABindingWithOnlyLiteralsNeedsNoResolver(t *testing.T) {
	r := resolver(t, registry(nil), nil)

	_, err := r.Resolve(context.Background(), request(binding(map[string]value.Ref{
		"namespace": value.Literal("payments"),
	})))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestNoTargetCarriesACriticalityLabel(t *testing.T) {
	// v2 scores risk from the plan (internal/policy/risk). A per-target label
	// here would be a second risk input that could disagree with the first,
	// which is the parallel authorization system spec 12.4 forbids.
	var built config.Target
	r := resolver(t, registry(&built), nil)

	if _, err := r.Resolve(context.Background(), request(binding(nil))); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if built.Criticality != "" {
		t.Errorf("criticality %q, want none", built.Criticality)
	}
}

func TestAResolverNeedsARegistry(t *testing.T) {
	if _, err := targets.New(nil, nil); err == nil {
		t.Fatal("a resolver with no registry was built anyway")
	}
}

func TestTheResolverIsWhatThePlannerAsksFor(t *testing.T) {
	var _ planner.Targets = resolver(t, registry(nil), nil)
}
