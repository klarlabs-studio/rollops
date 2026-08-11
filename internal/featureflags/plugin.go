package featureflags

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/pluginhost"
	pub "go.klarlabs.de/rollops/pkg/plugin"
)

// pluginProvider drives a flag plugin's apply_flag tool. It satisfies Provider.
type pluginProvider struct {
	proc *pluginhost.Process
}

func (p *pluginProvider) ApplyFlag(ctx context.Context, c Change) error {
	in, _ := json.Marshal(pub.FlagChange{
		Flag: c.Flag, Environment: c.Environment, Percentage: c.Percentage, Disabled: c.Disabled,
	})
	_, err := p.proc.Client.Invoke(ctx, pub.CapabilityFeatureFlag, pub.ToolApplyFlag, in)
	return err
}

// Close tears the flag plugin subprocess down.
func (p *pluginProvider) Close() error { return p.proc.Close() }

// BuildProvider launches the configured feature-flag plugin and returns a
// Provider backed by it. The caller must Close the returned io.Closer when done
// (the engine closes it after the rollout phase that used it). The binary is
// sha256-verified and its manifest validated against the plugin safety policy,
// and must declare the "featureflag" capability.
//
// ctx bounds launching the subprocess and reading its manifest. Both previously used
// context.Background(), so a caller that cancelled — a rollout being abandoned, a
// daemon shutting down — still waited out the plugin's own timeouts on a subprocess
// that may never answer. The engine's two call sites had a ctx in scope and were
// dropping it.
func BuildProvider(ctx context.Context, cfg *config.FeatureFlags) (Provider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("featureflags: nil config")
	}
	real, err := filepath.EvalSymlinks(cfg.Plugin)
	if err != nil {
		return nil, fmt.Errorf("featureflags: resolve plugin: %w", err)
	}
	if err := pluginhost.VerifyArtifact(real, cfg.SHA256); err != nil {
		return nil, fmt.Errorf("featureflags: %w", err)
	}
	policy := pluginhost.DefaultPolicy()
	proc, err := pluginhost.Launch(ctx, real, policy.AllowedEnvVars)
	if err != nil {
		return nil, fmt.Errorf("featureflags: %w", err)
	}
	// The manifest bound stays relative to now rather than inheriting a caller deadline
	// wholesale, but a caller whose deadline is sooner still wins — the shorter of the
	// two applies, which is what WithTimeout on a ctx that already has a deadline does.
	mctx, cancel := context.WithTimeout(ctx, pluginhost.ManifestTimeout)
	m, err := proc.Client.Manifest(mctx)
	cancel()
	if err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("featureflags: %w", err)
	}
	if err := policy.Validate(m); err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("featureflags: %w", err)
	}
	if !pluginhost.HasCapability(m, pub.CapabilityFeatureFlag) {
		_ = proc.Close()
		return nil, fmt.Errorf("featureflags: plugin %q does not declare the %q capability", m.Name, pub.CapabilityFeatureFlag)
	}
	return &pluginProvider{proc: proc}, nil
}
