package governance

import (
	"os"
	"strings"
	"testing"
)

// Rollops does not depend on any particular governance product, and no governance
// product depends on Rollops. They are separate products with separate release
// cadences, and a user of either must never be obliged to adopt the other.
//
// The decision is recorded in memory/decisions.md and docs/external-governance.md.
// This test is what keeps it true: a documented constraint decays the first time
// someone reaches for a convenient import, and the failure is silent — the code
// compiles, the tests pass, and the coupling is only noticed at release time.
//
// The provider stays generic on purpose. `Provider` is an interface satisfied by an
// HTTP client pointed at a URL, so anything answering the documented contract works.
// A named integration in this tree would be the coupling, whether or not it imported
// anything.
func TestNoGovernorDependency(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	gomod := string(data)

	// Products whose governance contract Rollops may speak, and must not import.
	// Listing them by name is deliberate: a generic check cannot tell a governance
	// product from any other dependency, and the point is to catch the specific
	// import someone would reach for.
	forbidden := []string{
		"relicta-tech/relicta",
		"klarlabs.de/relicta",
	}

	for _, module := range forbidden {
		if strings.Contains(gomod, module) {
			t.Errorf("go.mod references %q. Governance crosses a wire protocol, not a "+
				"module boundary: importing a governor couples release cadences, makes "+
				"its dependency graph this project's problem, and obliges every Rollops "+
				"user to acquire it. Implement the documented contract over HTTP "+
				"instead — see docs/external-governance.md.", module)
		}
	}
}

// The default must remain "no governance configured, nothing blocked". A user who
// has not asked for a governor must be unaffected by this integration existing.
func TestHookAllowsWithoutAProvider(t *testing.T) {
	decision, err := Hook{}.Evaluate(t.Context(), Request{
		Action:    "deploy",
		TargetRef: "widget-production",
	})
	if err != nil {
		t.Fatalf("Evaluate with no provider: %v", err)
	}
	if !decision.Allowed {
		t.Error("a Hook with no provider must allow: governance that was never " +
			"requested must not block a deployment")
	}
}
