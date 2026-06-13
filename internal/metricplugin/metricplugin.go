// Package metricplugin launches a metricprovider-capability plugin and adapts it
// to analysis.MetricsProvider, so rollout analysis can run against any metrics
// backend (Datadog, CloudWatch, a custom service) shipped as a sha256-pinned
// plugin — not only the built-in Prometheus provider. It lives apart from
// internal/analysis because internal/config depends on analysis; this package
// depends on both.
package metricplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"go.klarlabs.de/rollops/internal/analysis"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/pluginhost"
	pub "go.klarlabs.de/rollops/pkg/plugin"
)

// provider drives a metricprovider plugin's query_metric tool. Satisfies
// analysis.MetricsProvider.
type provider struct {
	proc *pluginhost.Process
}

func (p *provider) Query(ctx context.Context, query string) (float64, error) {
	in, _ := json.Marshal(pub.MetricQuery{Query: query})
	out, err := p.proc.Client.Invoke(ctx, pub.CapabilityMetricProvider, pub.ToolQueryMetric, in)
	if err != nil {
		return 0, err
	}
	var res pub.MetricResult
	if err := json.Unmarshal(out, &res); err != nil {
		return 0, fmt.Errorf("analysis: decode metric result: %w", err)
	}
	return res.Value, nil
}

// Close tears the metric-provider plugin subprocess down.
func (p *provider) Close() error { return p.proc.Close() }

// Build launches the configured metricprovider plugin and returns a
// MetricsProvider backed by it. The caller must Close the returned provider when
// done. The binary is sha256-verified and its manifest validated against the
// plugin safety policy, and must declare the "metricprovider" capability.
func Build(cfg *config.Analysis) (analysis.MetricsProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("analysis: nil config")
	}
	real, err := filepath.EvalSymlinks(cfg.Plugin)
	if err != nil {
		return nil, fmt.Errorf("analysis: resolve plugin: %w", err)
	}
	if err := pluginhost.VerifyBinary(real, cfg.SHA256); err != nil {
		return nil, fmt.Errorf("analysis: %w", err)
	}
	proc, err := pluginhost.Launch(context.Background(), real)
	if err != nil {
		return nil, fmt.Errorf("analysis: %w", err)
	}
	m, err := proc.Client.Manifest(context.Background())
	if err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("analysis: %w", err)
	}
	if err := pluginhost.DefaultPolicy().Validate(m); err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("analysis: %w", err)
	}
	if !pluginhost.HasCapability(m, pub.CapabilityMetricProvider) {
		_ = proc.Close()
		return nil, fmt.Errorf("analysis: plugin %q does not declare the %q capability", m.Name, pub.CapabilityMetricProvider)
	}
	return &provider{proc: proc}, nil
}
