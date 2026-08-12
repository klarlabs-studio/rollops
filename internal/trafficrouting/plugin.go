package trafficrouting

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/pluginhost"
	pub "go.klarlabs.de/rollops/pkg/plugin"
)

// pluginRouter drives a traffic-router plugin's set_weight tool. Satisfies Router.
type pluginRouter struct {
	proc *pluginhost.Process
}

func (p *pluginRouter) SetWeight(ctx context.Context, c Change) error {
	in, _ := json.Marshal(pub.TrafficChange{
		Route: c.Route, Namespace: c.Namespace,
		StableService: c.StableService, CanaryService: c.CanaryService, Weight: c.Weight,
	})
	_, err := p.proc.Client.Invoke(ctx, pub.CapabilityTrafficRouter, pub.ToolSetWeight, in)
	return err
}

// Close tears the traffic-router plugin subprocess down.
func (p *pluginRouter) Close() error { return p.proc.Close() }

// BuildRouter launches the configured traffic-router plugin and returns a Router
// backed by it. The caller must Close the returned router when done. The binary
// is sha256-verified and its manifest validated against the plugin safety
// policy, and must declare the "trafficrouter" capability.
func BuildRouter(ctx context.Context, cfg *config.TrafficRouting) (Router, error) {
	if cfg == nil {
		return nil, fmt.Errorf("trafficrouting: nil config")
	}
	// Built-in providers need no plugin binary.
	switch cfg.Provider {
	case "":
		// plugin mode — fall through
	case "gateway":
		return newGatewayRouter(cfg.Kubeconfig, cfg.Context), nil
	default:
		return nil, fmt.Errorf("trafficrouting: unknown provider %q", cfg.Provider)
	}
	real, err := filepath.EvalSymlinks(cfg.Plugin)
	if err != nil {
		return nil, fmt.Errorf("trafficrouting: resolve plugin: %w", err)
	}
	if err := pluginhost.VerifyArtifact(real, cfg.SHA256); err != nil {
		return nil, fmt.Errorf("trafficrouting: %w", err)
	}
	policy := pluginhost.DefaultPolicy()
	proc, err := pluginhost.Launch(ctx, real, policy.AllowedEnvVars)
	if err != nil {
		return nil, fmt.Errorf("trafficrouting: %w", err)
	}
	mctx, cancel := context.WithTimeout(ctx, pluginhost.ManifestTimeout)
	m, err := proc.Client.Manifest(mctx)
	cancel()
	if err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("trafficrouting: %w", err)
	}
	if err := policy.Validate(m); err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("trafficrouting: %w", err)
	}
	if !pluginhost.HasCapability(m, pub.CapabilityTrafficRouter) {
		_ = proc.Close()
		return nil, fmt.Errorf("trafficrouting: plugin %q does not declare the %q capability", m.Name, pub.CapabilityTrafficRouter)
	}
	return &pluginRouter{proc: proc}, nil
}
