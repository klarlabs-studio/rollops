// Package plugin builds a pkg/target.Target backed by a third-party plugin
// subprocess that declares the "target" capability. It is a thin adapter over
// internal/pluginhost (launch, manifest, safety) plus the target tool wire.
package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/pluginhost"
	pub "go.klarlabs.de/rollops/pkg/plugin"
	pt "go.klarlabs.de/rollops/pkg/target"
	"go.klarlabs.de/rollops/pkg/target/rollopstargetv2"
	"go.klarlabs.de/rollops/pkg/target/v1adapter"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
	"go.klarlabs.de/rollops/pkg/target/v2grpc"
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
	proc, _, err := launch(cfg)
	if err != nil {
		return nil, err
	}
	return &adapter{proc: proc}, nil
}

// launch does everything up to having a verified, policy-approved plugin that
// declares the target capability: pin check, subprocess, manifest, policy. Both
// build paths need all of it, and the order matters — nothing is launched until
// the binary matches its pin, and nothing is used until the manifest clears the
// policy.
func launch(cfg config.Target) (*pluginhost.Process, pub.Manifest, error) {
	fail := func(err error) (*pluginhost.Process, pub.Manifest, error) {
		return nil, pub.Manifest{}, fmt.Errorf("plugin: target %q: %w", cfg.Ref, err)
	}

	binary, _ := cfg.Spec["binary"].(string)
	if binary == "" {
		return fail(errors.New("spec.binary is required"))
	}
	real, err := filepath.EvalSymlinks(binary)
	if err != nil {
		return fail(fmt.Errorf("resolve binary: %w", err))
	}
	pin, _ := cfg.Spec["sha256"].(string)
	if err := pluginhost.VerifyArtifact(real, pin); err != nil {
		return fail(err)
	}
	policy := pluginhost.DefaultPolicy()
	proc, err := pluginhost.Launch(context.Background(), real, policy.AllowedEnvVars)
	if err != nil {
		return fail(err)
	}
	mctx, cancel := context.WithTimeout(context.Background(), pluginhost.ManifestTimeout)
	m, err := proc.Client.Manifest(mctx)
	cancel()
	if err != nil {
		_ = proc.Close()
		return fail(err)
	}
	if err := policy.Validate(m); err != nil {
		_ = proc.Close()
		return fail(err)
	}
	if !pluginhost.HasCapability(m, pub.CapabilityTarget) {
		_ = proc.Close()
		return fail(fmt.Errorf("plugin %q does not declare the %q capability", m.Name, pub.CapabilityTarget))
	}
	return proc, m, nil
}

// BuildV2 constructs a plugin-backed v2 target. A plugin that declares the
// target contract is reached over the typed service; one that does not is
// reached over the generic tool wire and adapted, so a plugin written before v2
// keeps working without its author doing anything (ADR-0006, §9.6).
//
// Capabilities are resolved, never asserted. The plugin's declared ceiling is
// narrowed by what the bound target claims now, and the engine acts on the
// intersection — which is why this returns a *targetv2.Bound rather than a
// Target: the optional verbs are reachable only through it.
func BuildV2(cfg config.Target) (*targetv2.Bound, error) {
	proc, m, err := launch(cfg)
	if err != nil {
		return nil, err
	}

	contract, typed := m.Contract(pub.ContractTarget)
	if typed && contract.Version != 2 {
		_ = proc.Close()
		return nil, fmt.Errorf("plugin: target %q: plugin %q serves target contract v%d, which this host does not speak",
			cfg.Ref, m.Name, contract.Version)
	}

	meta := targetv2.Metadata{Kind: "plugin", Name: cfg.Ref, Version: m.Version}

	var inner targetv2.Target
	if typed {
		inner = v2grpc.NewClient(rollopstargetv2.NewTargetClient(proc.Conn()), meta)
	} else {
		inner = v1adapter.New(&adapter{proc: proc}, meta)
	}
	// The subprocess is owned by the transport, not by the target, so closing
	// it is a step on the binding rather than a wrapper around the target —
	// anything in between would swallow the optional capabilities.
	teardown := targetv2.OnClose(proc.Close)

	cctx, cancel := context.WithTimeout(context.Background(), pluginhost.ManifestTimeout)
	claimed, err := inner.Capabilities(cctx)
	cancel()
	if err != nil {
		_ = proc.Close()
		return nil, fmt.Errorf("plugin: target %q: capabilities: %w", cfg.Ref, err)
	}

	// A v1 plugin has no ceiling because there was nowhere for its author to
	// declare one, and the adapter derives the claim from what the v1 target
	// actually implements — so there is nothing to over-claim and the claim
	// stands. That is the truthful outcome, not a gap: nothing was authorized
	// separately, so nothing is refused separately.
	if !typed {
		return targetv2.NewBound(inner, claimed, teardown), nil
	}
	ceiling, _ := targetv2.ParseCapabilities(contract.Capabilities)
	return targetv2.NewNarrowedBound(inner, ceiling, claimed, teardown), nil
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
