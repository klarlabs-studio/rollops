package engine

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/analysis"
	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
)

const analysisPluginYAML = `
apiVersion: rollops.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: analyzed
spec:
  target:
    kind: fake
    ref: analyzed/prod/api
    criticality: low
    spec:
      x: 1
  strategy:
    type: rolling
  rollback:
    auto: true
  analysis:
    provider: datadog
    plugin: /unused/in/test
    sha256: deadbeef
    metrics:
      - {name: errorRate, query: "avg:trace.errors{service:api}"}
    condition: "errorRate < 0.05"
    count: 2
    failureLimit: 1
`

func TestPipeline_MetricPluginProviderRollsBack(t *testing.T) {
	c, err := config.Load([]byte(analysisPluginYAML))
	if err != nil {
		t.Fatal(err)
	}
	var built int
	e, _, _ := wiredEngine(t,
		WithMetricAnalysis(),
		WithMetricsProviderBuilder(func(_ context.Context, a *config.Analysis) (analysis.MetricsProvider, error) {
			built++
			if a.Plugin == "" {
				t.Error("metricsBuild called without a plugin path")
			}
			return fixedMetrics(0.2), nil // breaches errorRate < 0.05
		}),
	)
	ctx := context.Background()
	r, err := e.Apply(ctx, ApplyRequest{Config: c})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	out, err := e.VerifyOrRollback(ctx, r.ID, pt.Manifest{Kind: "fake", Checksum: "prior"}, c)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if built == 0 {
		t.Fatal("the metricprovider plugin builder was never invoked")
	}
	if !out.RolledBack || !strings.Contains(out.Reason, "analysis") {
		t.Fatalf("plugin-backed analysis breach should roll back; got rolledBack=%v reason=%q", out.RolledBack, out.Reason)
	}
}
