package plugin_test

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	pub "go.klarlabs.de/rollops/pkg/plugin"
	"go.klarlabs.de/rollops/pkg/plugin/rollopspluginv1"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// nullTarget is a v2 target that does nothing. These tests are about what a
// plugin declares and registers, not about what it deploys.
type nullTarget struct{}

func (nullTarget) Metadata() targetv2.Metadata {
	return targetv2.Metadata{Kind: "null", Name: "x/test/null", Version: "1"}
}

func (nullTarget) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return targetv2.Capabilities{HealthObservation: true}, nil
}

func (nullTarget) Inspect(context.Context, targetv2.InspectRequest) (targetv2.ObservedState, error) {
	return targetv2.ObservedState{}, nil
}

func (nullTarget) Plan(context.Context, targetv2.PlanRequest) (targetv2.PlanResult, error) {
	return targetv2.PlanResult{}, nil
}

func (nullTarget) Apply(context.Context, targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	return targetv2.ApplyResult{}, nil
}

func (nullTarget) Observe(context.Context, targetv2.ObserveRequest) (targetv2.Observation, error) {
	return targetv2.Observation{}, nil
}

func (nullTarget) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	return targetv2.RollbackResult{}, targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)
}

func manifestOf(t *testing.T, srv *pub.Server) *rollopspluginv1.GetManifestResponse {
	t.Helper()
	res, err := srv.GetManifest(context.Background(), &rollopspluginv1.GetManifestRequest{})
	if err != nil {
		t.Fatalf("GetManifest: %v", err)
	}
	return res
}

// TestServingAContractDeclaresIt is the invariant the two-argument form exists
// for: a host is told where to look by the manifest and nothing else, so a
// service registered without a declaration is a service nobody calls.
func TestServingAContractDeclaresIt(t *testing.T) {
	var registered bool
	srv := pub.NewServer(pub.NewManifest("acme/one", "1.0.0").Build()).
		ServeContract("target", 2, func(grpc.ServiceRegistrar) { registered = true })

	got := manifestOf(t, srv).GetContracts()
	if len(got) != 1 {
		t.Fatalf("declared %d contracts, want 1", len(got))
	}
	if got[0].GetKind() != "target" || got[0].GetVersion() != 2 {
		t.Errorf("declared %q v%d, want target v2", got[0].GetKind(), got[0].GetVersion())
	}
	if registered {
		t.Error("the service registered before anything was serving")
	}

	srv.RegisterContracts(fakeRegistrar{})
	if !registered {
		t.Error("the declared service was never registered")
	}
}

type fakeRegistrar struct{}

func (fakeRegistrar) RegisterService(*grpc.ServiceDesc, any) {}

func TestAPluginWithNoTypedServiceDeclaresNoContract(t *testing.T) {
	srv := pub.NewServer(pub.NewManifest("acme/plain", "1.0.0").Build())
	if got := manifestOf(t, srv).GetContracts(); len(got) != 0 {
		t.Errorf("a plain plugin declared %d contracts, want none", len(got))
	}
}

// TestATargetV2PluginAdvertisesTheTypedContract covers what a target plugin
// author actually writes. The generic capability is still declared, because
// that is what the safety policy ranks; the contract is what says the verbs
// arrive over a typed service rather than as JSON tool calls.
func TestATargetV2PluginAdvertisesTheTypedContract(t *testing.T) {
	srv := pub.NewTargetV2Server("acme/exotic", "1.0.0", nullTarget{},
		pub.Safety{RiskClass: pub.RiskActive})

	m := manifestOf(t, srv)

	if !hasCapability(m, pub.CapabilityTarget) {
		t.Errorf("a target plugin did not declare the %q capability", pub.CapabilityTarget)
	}
	contracts := m.GetContracts()
	if len(contracts) != 1 || contracts[0].GetKind() != pub.ContractTarget || contracts[0].GetVersion() != 2 {
		t.Fatalf("contracts = %v, want target v2", contracts)
	}
	if m.GetSafety().GetRiskClass() != string(pub.RiskActive) {
		t.Errorf("risk class %q did not survive", m.GetSafety().GetRiskClass())
	}
}

// TestNoV2ToolsAreAdvertisedGenerically keeps the two wires from being confused
// for one another. Listing apply as a tool would invite a host to invoke it
// through the generic service, where nothing is listening.
func TestNoV2ToolsAreAdvertisedGenerically(t *testing.T) {
	srv := pub.NewTargetV2Server("acme/exotic", "1.0.0", nullTarget{}, pub.Safety{})

	for _, c := range manifestOf(t, srv).GetCapabilities() {
		if c.GetName() != pub.CapabilityTarget {
			continue
		}
		if n := len(c.GetTools()); n != 0 {
			t.Errorf("the v2 target capability advertised %d generic tools, want none", n)
		}
	}
	if _, err := srv.InvokeTool(context.Background(), &rollopspluginv1.InvokeToolRequest{
		Capability: pub.CapabilityTarget, Tool: pub.ToolApply,
	}); err == nil {
		t.Error("apply was reachable through the generic service")
	}
}

func hasCapability(m *rollopspluginv1.GetManifestResponse, name string) bool {
	for _, c := range m.GetCapabilities() {
		if c.GetName() == name {
			return true
		}
	}
	return false
}
