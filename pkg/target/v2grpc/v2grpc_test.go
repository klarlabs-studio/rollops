package v2grpc_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"go.klarlabs.de/rollops/pkg/target/rollopstargetv2"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
	"go.klarlabs.de/rollops/pkg/target/v2grpc"
)

// fake is the far side of the wire. Every method answers from a field, so a
// test says what the plugin does without a mock framework.
type fake struct {
	caps targetv2.Capabilities

	inspect  targetv2.ObservedState
	plan     targetv2.PlanResult
	apply    targetv2.ApplyResult
	observe  targetv2.Observation
	drift    targetv2.DriftResult
	pruned   int
	lastReq  targetv2.ApplyRequest
	lastDrft targetv2.DriftRequest

	err error // returned by every method when non-nil
}

func (f *fake) Metadata() targetv2.Metadata {
	return targetv2.Metadata{Kind: "fake", Name: "x/test/one", Version: "9"}
}

func (f *fake) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return f.caps, f.err
}

func (f *fake) Inspect(context.Context, targetv2.InspectRequest) (targetv2.ObservedState, error) {
	return f.inspect, f.err
}

func (f *fake) Plan(context.Context, targetv2.PlanRequest) (targetv2.PlanResult, error) {
	return f.plan, f.err
}

func (f *fake) Apply(_ context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	f.lastReq = req
	return f.apply, f.err
}

func (f *fake) Observe(context.Context, targetv2.ObserveRequest) (targetv2.Observation, error) {
	return f.observe, f.err
}

func (f *fake) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return targetv2.RollbackResult{Changed: true, Detail: "rolled back"}, f.err
}

func (f *fake) Promote(context.Context, targetv2.PromoteRequest) (targetv2.PromoteResult, error) {
	return targetv2.PromoteResult{Detail: "promoted"}, f.err
}

func (f *fake) DetectDrift(_ context.Context, req targetv2.DriftRequest) (targetv2.DriftResult, error) {
	f.lastDrft = req
	return f.drift, f.err
}

func (f *fake) Prune(context.Context, targetv2.PruneRequest) (targetv2.PruneResult, error) {
	f.pruned++
	return targetv2.PruneResult{Removed: 4}, f.err
}

// bare implements the mandatory contract and none of the optional
// subinterfaces, which is what a v1-era target looks like on the plugin side.
type bare struct{}

func (b *bare) Metadata() targetv2.Metadata { return targetv2.Metadata{Kind: "bare", Name: "x/b"} }

func (b *bare) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return targetv2.Capabilities{}, nil
}

func (b *bare) Inspect(context.Context, targetv2.InspectRequest) (targetv2.ObservedState, error) {
	return targetv2.ObservedState{}, nil
}

func (b *bare) Plan(context.Context, targetv2.PlanRequest) (targetv2.PlanResult, error) {
	return targetv2.PlanResult{}, nil
}

func (b *bare) Apply(context.Context, targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	return targetv2.ApplyResult{}, nil
}

func (b *bare) Observe(context.Context, targetv2.ObserveRequest) (targetv2.Observation, error) {
	return targetv2.Observation{}, nil
}

func (b *bare) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
}

// rawFake serves the generated interface directly, for the cases where the
// point is what a plugin the host did not write puts on the wire.
type rawFake struct {
	rollopstargetv2.UnimplementedTargetServer
	caps []string
}

func (r *rawFake) GetCapabilities(context.Context, *rollopstargetv2.GetCapabilitiesRequest) (*rollopstargetv2.GetCapabilitiesResponse, error) {
	return &rollopstargetv2.GetCapabilitiesResponse{Capabilities: r.caps}, nil
}

// dial serves impl over an in-process connection and hands back the host-side
// view of it. Everything a test asserts therefore crossed a real wire.
func dial(t *testing.T, impl targetv2.Target) targetv2.Target {
	t.Helper()
	return dialRaw(t, func(g *grpc.Server) {
		rollopstargetv2.RegisterTargetServer(g, v2grpc.NewServer(impl))
	})
}

func dialRaw(t *testing.T, register func(*grpc.Server)) targetv2.Target {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	g := grpc.NewServer()
	register(g)
	go func() { _ = g.Serve(lis) }()
	t.Cleanup(g.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return v2grpc.NewClient(rollopstargetv2.NewTargetClient(conn),
		targetv2.Metadata{Kind: "fake", Name: "x/test/one", Version: "9"})
}

func TestAnUnsupportedCapabilityStaysUnsupportedAcrossTheWire(t *testing.T) {
	// The whole point of making the optional methods mandatory on the wire:
	// "I cannot do this" has to arrive as itself, not as a generic failure the
	// caller reads as a broken plugin.
	c := dial(t, &bare{})

	_, err := c.(targetv2.Pruner).Prune(context.Background(), targetv2.PruneRequest{})

	if !targetv2.IsUnsupported(err) {
		t.Fatalf("Prune gave %v, want unsupported", err)
	}
	var te *targetv2.Error
	if !errors.As(err, &te) || te.Capability != targetv2.CapabilityPrune {
		t.Errorf("the error names capability %q, want %q", te.Capability, targetv2.CapabilityPrune)
	}
}

func TestEveryKindSurvivesTheWire(t *testing.T) {
	// A gRPC code is the only thing a status carries by default, and there are
	// fewer codes than kinds. A kind that arrives as something else is a caller
	// retrying what it should have refused, or refusing what it should retry.
	kinds := []targetv2.Kind{
		targetv2.KindUnsupported,
		targetv2.KindInvalid,
		targetv2.KindNotFound,
		targetv2.KindDenied,
		targetv2.KindConflict,
		targetv2.KindUnavailable,
		targetv2.KindTimeout,
		targetv2.KindCanceled,
		targetv2.KindInternal,
	}
	for _, kind := range kinds {
		t.Run(string(kind), func(t *testing.T) {
			c := dial(t, &fake{err: targetv2.Failf(kind, "Inspect", nil, "boom")})

			_, err := c.Inspect(context.Background(), targetv2.InspectRequest{})

			if got := targetv2.KindOf(err); got != kind {
				t.Errorf("kind %q arrived as %q", kind, got)
			}
		})
	}
}

func TestAnIdempotencyConflictKeepsItsKey(t *testing.T) {
	// Without the key the host cannot say which operation it double-sent, and
	// the conflict is unactionable.
	c := dial(t, &fake{err: targetv2.IdempotencyConflict("Apply", "dep-1/op-1")})

	_, err := c.Apply(context.Background(), targetv2.ApplyRequest{
		Desired: targetv2.DesiredState{Checksum: "abc"}, IdempotencyKey: "dep-1/op-1",
	})

	var te *targetv2.Error
	if !errors.As(err, &te) {
		t.Fatalf("Apply gave %v, want a target error", err)
	}
	if te.Kind != targetv2.KindConflict || te.IdempotencyKey != "dep-1/op-1" {
		t.Errorf("the conflict arrived as %+v", te)
	}
}

func TestAPlainFailureFromAnOlderPluginStillHasAKind(t *testing.T) {
	// A plugin that attaches no detail is not a plugin whose errors are
	// kindless: the code alone is enough to place it.
	c := dialRaw(t, func(g *grpc.Server) {
		rollopstargetv2.RegisterTargetServer(g, &rawFake{})
	})

	_, err := c.Inspect(context.Background(), targetv2.InspectRequest{})

	if got := targetv2.KindOf(err); got != targetv2.KindUnsupported {
		t.Errorf("an unimplemented method gave kind %q, want %q", got, targetv2.KindUnsupported)
	}
}

func TestACapabilityNameTheHostDoesNotKnowIsIgnored(t *testing.T) {
	// A plugin may gain a capability in a patch release. Refusing the whole
	// answer because one name is new would break every host older than it.
	c := dialRaw(t, func(g *grpc.Server) {
		rollopstargetv2.RegisterTargetServer(g, &rawFake{caps: []string{"drift", "time-travel"}})
	})

	caps, err := c.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps != (targetv2.Capabilities{DriftDetection: true}) {
		t.Errorf("capabilities are %+v, want drift alone", caps)
	}
}

func TestCapabilitiesCrossAsNames(t *testing.T) {
	c := dial(t, &fake{caps: targetv2.Capabilities{DriftDetection: true, Prune: true}})

	caps, err := c.Capabilities(context.Background())
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if caps != (targetv2.Capabilities{DriftDetection: true, Prune: true}) {
		t.Errorf("capabilities are %+v, want drift and prune", caps)
	}
}

func TestTheDesiredStateArrivesByteForByte(t *testing.T) {
	// Spec is opaque to everything but the target that declared Kind. A wire
	// that reinterprets it has made the host care about the substrate.
	f := &fake{}
	c := dial(t, f)
	spec := []byte{0x00, 0xff, 0x7f, 'a'}

	if _, err := c.Apply(context.Background(), targetv2.ApplyRequest{
		Desired: targetv2.DesiredState{
			Kind:     "kubernetes",
			Spec:     spec,
			Checksum: "sha256:abc",
			Labels:   map[string]string{"env": "prod"},
		},
		IdempotencyKey: "dep-1/op-1",
	}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := f.lastReq
	if string(got.Desired.Spec) != string(spec) {
		t.Errorf("spec arrived as %v", got.Desired.Spec)
	}
	if got.Desired.Kind != "kubernetes" || got.Desired.Checksum != "sha256:abc" {
		t.Errorf("desired state arrived as %+v", got.Desired)
	}
	if got.Desired.Labels["env"] != "prod" {
		t.Errorf("labels arrived as %v", got.Desired.Labels)
	}
	if got.IdempotencyKey != "dep-1/op-1" {
		t.Errorf("idempotency key arrived as %q", got.IdempotencyKey)
	}
}

func TestAPlanCrossesWithItsBlockers(t *testing.T) {
	c := dial(t, &fake{plan: targetv2.PlanResult{
		Changes:  true,
		Diff:     "- replicas: 3",
		Rendered: []byte("rendered"),
		Blockers: []string{"rbac: cannot create middlewares"},
	}})

	res, err := c.Plan(context.Background(), targetv2.PlanRequest{})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !res.Changes || res.Diff != "- replicas: 3" || string(res.Rendered) != "rendered" {
		t.Errorf("plan arrived as %+v", res)
	}
	if len(res.Blockers) != 1 {
		t.Fatalf("blockers arrived as %v", res.Blockers)
	}
}

func TestTheInventoryCrossesWithItsOwnershipTree(t *testing.T) {
	c := dial(t, &fake{inspect: targetv2.ObservedState{
		Fingerprint: "abc",
		Resources: []targetv2.Resource{
			{Kind: "Deployment", Name: "api", Namespace: "prod", Status: "ready"},
			{Kind: "Pod", Name: "api-1", Namespace: "prod", Status: "ready", Parent: "api"},
		},
		Meta: map[string]string{"revision": "7"},
	}})

	state, err := c.Inspect(context.Background(), targetv2.InspectRequest{})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if state.Fingerprint != "abc" || len(state.Resources) != 2 {
		t.Fatalf("state arrived as %+v", state)
	}
	if state.Resources[1].Parent != "api" {
		t.Errorf("the ownership link was lost: %+v", state.Resources[1])
	}
	if state.Meta["revision"] != "7" {
		t.Errorf("meta arrived as %v", state.Meta)
	}
}

func TestHealthCrossesAsAVerdictNotAString(t *testing.T) {
	c := dial(t, &fake{observe: targetv2.Observation{
		Fingerprint: "abc",
		Health:      targetv2.HealthStatus{State: targetv2.HealthDegraded, Reason: "1/3 ready"},
	}})

	obs, err := c.Observe(context.Background(), targetv2.ObserveRequest{Handle: "h1"})
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.Health.State != targetv2.HealthDegraded || obs.Health.Reason != "1/3 ready" {
		t.Errorf("health arrived as %+v", obs.Health)
	}
}

func TestTheOptionalMethodsReachTheTarget(t *testing.T) {
	f := &fake{drift: targetv2.DriftResult{Drifted: true, Detail: "replicas 3 -> 5"}}
	c := dial(t, f)
	ctx := context.Background()

	drift, err := c.(targetv2.Drifter).DetectDrift(ctx, targetv2.DriftRequest{
		Desired: targetv2.DesiredState{Checksum: "abc"},
	})
	if err != nil {
		t.Fatalf("DetectDrift: %v", err)
	}
	if !drift.Drifted || f.lastDrft.Desired.Checksum != "abc" {
		t.Errorf("drift arrived as %+v from request %+v", drift, f.lastDrft)
	}

	pruned, err := c.(targetv2.Pruner).Prune(ctx, targetv2.PruneRequest{})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if pruned.Removed != 4 || f.pruned != 1 {
		t.Errorf("prune removed %d over %d calls", pruned.Removed, f.pruned)
	}

	promoted, err := c.(targetv2.Promoter).Promote(ctx, targetv2.PromoteRequest{Handle: "h1"})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if promoted.Detail != "promoted" {
		t.Errorf("promote arrived as %+v", promoted)
	}

	back, err := c.Rollback(ctx, targetv2.RollbackRequest{Handle: "h1"})
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !back.Changed || back.Detail != "rolled back" {
		t.Errorf("rollback arrived as %+v", back)
	}
}

func TestTheClientMetadataIsTheHostsNotTheWires(t *testing.T) {
	// Metadata is local by design: no call, nothing that can fail. The host
	// names the bound instance, because a plugin does not know how it was
	// named in configuration.
	c := dial(t, &fake{})

	if got := c.Metadata().Name; got != "x/test/one" {
		t.Errorf("metadata name is %q, want the host's", got)
	}
}

func TestACancelledCallIsNotReportedAsBroken(t *testing.T) {
	c := dial(t, &fake{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := c.Inspect(ctx, targetv2.InspectRequest{})

	if got := targetv2.KindOf(err); got != targetv2.KindCanceled {
		t.Errorf("a cancelled call gave kind %q, want %q", got, targetv2.KindCanceled)
	}
}

func TestTheClientSatisfiesTheWholeContract(t *testing.T) {
	c := dial(t, &fake{})
	if _, ok := c.(targetv2.Drifter); !ok {
		t.Errorf("the client cannot drift, so a capability check could be skipped by assertion")
	}
	if _, ok := c.(targetv2.Pruner); !ok {
		t.Errorf("the client cannot prune")
	}
	if _, ok := c.(targetv2.Promoter); !ok {
		t.Errorf("the client cannot promote")
	}
}
