// Package plugin builds a pkg/target.Target backed by a third-party plugin
// subprocess that declares the "target" capability. It is a thin adapter over
// internal/pluginhost (launch, manifest, safety) plus the target tool wire.
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/pluginhost"
	pub "go.klarlabs.de/rollops/pkg/plugin"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// Build constructs a plugin-backed target from a config target. The spec names
// the plugin binary and pins its sha256; the host launches it, validates the
// manifest against the safety policy, and requires a "target" capability:
//
//	target:
//	  kind: plugin
//	  ref: x/prod/exotic
//	  spec:
//	    binary: /usr/local/lib/rollops/plugins/exotic
//	    sha256: <hex of the binary>
//	    ... plugin-specific keys ...
func Build(cfg config.Target) (pt.Target, error) {
	binary, _ := cfg.Spec["binary"].(string)
	if binary == "" {
		return nil, fmt.Errorf("plugin: target %q: spec.binary is required", cfg.Ref)
	}
	real, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, fmt.Errorf("plugin: target %q: resolve binary: %w", cfg.Ref, err)
	}
	pin, _ := cfg.Spec["sha256"].(string)
	if err := pluginhost.VerifyArtifact(real, pin); err != nil {
		return nil, fmt.Errorf("plugin: target %q: %w", cfg.Ref, err)
	}
	policy := pluginhost.DefaultPolicy()
	proc, err := pluginhost.Launch(context.Background(), real, policy.AllowedEnvVars)
	if err != nil {
		return nil, fmt.Errorf("plugin: target %q: %w", cfg.Ref, err)
	}
	mctx, cancel := context.WithTimeout(context.Background(), pluginhost.ManifestTimeout)
	m, err := proc.Client.Manifest(mctx)
	cancel()
	if err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("plugin: target %q: %w", cfg.Ref, err)
	}
	if err := policy.Validate(m); err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("plugin: target %q: %w", cfg.Ref, err)
	}
	if !pluginhost.HasCapability(m, pub.CapabilityTarget) {
		_ = proc.Close()
		return nil, fmt.Errorf("plugin: target %q: plugin %q does not declare the %q capability", cfg.Ref, m.Name, pub.CapabilityTarget)
	}
	return &adapter{proc: proc}, nil
}

// adapter turns target-capability tool invocations into a pt.Target.
type adapter struct {
	proc *pluginhost.Process
}

func (a *adapter) Apply(ctx context.Context, m pt.Manifest) (pt.Result, error) {
	in, _ := json.Marshal(pub.ApplyInput{Kind: m.Kind, Spec: m.Spec, Checksum: m.Checksum})
	out, err := a.proc.Client.Invoke(ctx, pub.CapabilityTarget, pub.ToolApply, in)
	if err != nil {
		return pt.Result{}, err
	}
	var res pub.ApplyOutput
	if err := json.Unmarshal(out, &res); err != nil {
		return pt.Result{}, fmt.Errorf("plugin: apply: %w", err)
	}
	return pt.Result{Changed: res.Changed, Detail: res.Detail}, nil
}

func (a *adapter) Observe(ctx context.Context) (pt.Fingerprint, error) {
	out, err := a.proc.Client.Invoke(ctx, pub.CapabilityTarget, pub.ToolObserve, []byte("{}"))
	if err != nil {
		return pt.Fingerprint{}, err
	}
	var res pub.ObserveOutput
	if err := json.Unmarshal(out, &res); err != nil {
		return pt.Fingerprint{}, fmt.Errorf("plugin: observe: %w", err)
	}
	return pt.Fingerprint{Value: res.Value, Meta: res.Meta}, nil
}

func (a *adapter) Health(ctx context.Context) (pt.HealthStatus, error) {
	out, err := a.proc.Client.Invoke(ctx, pub.CapabilityTarget, pub.ToolHealth, []byte("{}"))
	if err != nil {
		return pt.HealthStatus{}, err
	}
	var res pub.HealthOutput
	if err := json.Unmarshal(out, &res); err != nil {
		return pt.HealthStatus{}, fmt.Errorf("plugin: health: %w", err)
	}
	state := pt.HealthState(res.State)
	switch state {
	case pt.HealthHealthy, pt.HealthDegraded, pt.HealthUnhealthy:
		return pt.HealthStatus{State: state, Reason: res.Reason}, nil
	default:
		return pt.HealthStatus{}, fmt.Errorf("plugin: invalid health state %d", res.State)
	}
}

// Close tears the plugin subprocess down.
func (a *adapter) Close() error { return a.proc.Close() }
