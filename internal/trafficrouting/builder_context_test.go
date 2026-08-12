package trafficrouting

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/config"
)

// BuildRouter hardcoded context.Background() for both launching the plugin subprocess
// and reading its manifest, so a caller that cancelled — a rollout being abandoned, a
// daemon shutting down — waited out the plugin's own timeouts regardless. The engine's
// call site (driveTraffic) had a ctx in scope and was dropping it.
//
// Fixed in #113 for featureflags.BuildProvider; this is the same defect in the second of
// three identical builders, which that PR missed.
//
// That the engine's context actually arrives is asserted in the engine package, where the
// builder seam can observe what it is handed. It cannot usefully be asserted here: the
// sha256 verification runs before the launch, so a cancelled context never reaches the
// code that would honour it, and a test written at this level would pass without
// exercising anything.

// A built-in provider needs no plugin and no context work at all. Guarded so the
// signature change does not quietly make the built-in path depend on a live context.
func TestTheBuiltInGatewayProviderIgnoresContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	router, err := BuildRouter(ctx, &config.TrafficRouting{Provider: "gateway"})
	if err != nil {
		t.Fatalf("the built-in gateway router must build without a plugin or a live "+
			"context: %v", err)
	}
	if router == nil {
		t.Fatal("BuildRouter returned no router and no error")
	}
}

func TestAnUnknownProviderIsRefusedByName(t *testing.T) {
	_, err := BuildRouter(context.Background(), &config.TrafficRouting{Provider: "istio-but-typo"})
	if err == nil {
		t.Fatal("an unrecognized provider must be refused rather than falling through to " +
			"plugin mode, where it would fail with a confusing message about a missing binary")
	}
	if !strings.Contains(err.Error(), "istio-but-typo") {
		t.Errorf("error = %q, want it to name the unrecognized provider so the typo is "+
			"visible", err)
	}
}

// baseArgs decides which cluster kubectl talks to, and was entirely untested. Getting it
// wrong does not fail loudly: it shifts production traffic on whatever cluster the
// ambient kubeconfig happens to point at.
func TestBaseArgsTargetsTheConfiguredCluster(t *testing.T) {
	cases := []struct {
		name       string
		kubeconfig string
		context    string
		want       []string
	}{
		{"neither set uses the ambient kubeconfig", "", "", nil},
		{"kubeconfig only", "/tmp/kc", "", []string{"--kubeconfig", "/tmp/kc"}},
		{"context only", "", "prod", []string{"--context", "prod"}},
		{"both", "/tmp/kc", "prod", []string{"--kubeconfig", "/tmp/kc", "--context", "prod"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := newGatewayRouter(tc.kubeconfig, tc.context).baseArgs()
			if len(got) != len(tc.want) {
				t.Fatalf("baseArgs() = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("baseArgs() = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// The flags must reach kubectl, not merely be computed. A baseArgs that is correct and
// unused targets the ambient cluster while looking configured.
func TestTheConfiguredClusterReachesKubectl(t *testing.T) {
	r := newGatewayRouter("/tmp/kc", "prod")

	var seen []string
	r.run = func(_ context.Context, _ []byte, args ...string) (string, error) {
		seen = append(seen, args...)
		return "{}", nil
	}

	// Any call that shells out is enough to observe the flags.
	_, _ = r.run(context.Background(), nil, r.baseArgs()...)

	var sawKubeconfig, sawContext bool
	for i, a := range seen {
		if a == "--kubeconfig" && i+1 < len(seen) && seen[i+1] == "/tmp/kc" {
			sawKubeconfig = true
		}
		if a == "--context" && i+1 < len(seen) && seen[i+1] == "prod" {
			sawContext = true
		}
	}
	if !sawKubeconfig || !sawContext {
		t.Errorf("kubectl was invoked with %v: the configured kubeconfig and context must "+
			"reach it, or traffic shifts on whichever cluster the ambient config names", seen)
	}
}
