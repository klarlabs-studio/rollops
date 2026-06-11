package plugin

import (
	"context"
	"encoding/json"

	pt "go.klarlabs.de/rollops/pkg/target"
)

// ServeTarget runs a pkg/target.Target as a Rollops target plugin. It builds the
// "target" capability manifest and wires apply/observe/health, so a target
// plugin's main is one line:
//
//	func main() { panic(plugin.ServeTarget("acme/exotic", "1.0.0", newTarget(), plugin.Safety{RiskClass: plugin.RiskActive})) }
func ServeTarget(name, version string, t pt.Target, safety Safety) error {
	m := NewManifest(name, version).
		Capability(CapabilityTarget, "Deployment target").
		Tool(ToolApply, "Deploy desired state", true).
		Tool(ToolObserve, "Report live fingerprint", false).
		Tool(ToolHealth, "Report health", false).
		Done().
		Safety(safety).
		Build()

	srv := NewServer(m).
		HandleTool(CapabilityTarget, ToolApply, func(ctx context.Context, in []byte) ([]byte, error) {
			var req ApplyInput
			if err := json.Unmarshal(in, &req); err != nil {
				return nil, err
			}
			res, err := t.Apply(ctx, pt.Manifest{Kind: req.Kind, Spec: req.Spec, Checksum: req.Checksum})
			if err != nil {
				return nil, err
			}
			return json.Marshal(ApplyOutput{Changed: res.Changed, Detail: res.Detail})
		}).
		HandleTool(CapabilityTarget, ToolObserve, func(ctx context.Context, _ []byte) ([]byte, error) {
			fp, err := t.Observe(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(ObserveOutput{Value: fp.Value, Meta: fp.Meta})
		}).
		HandleTool(CapabilityTarget, ToolHealth, func(ctx context.Context, _ []byte) ([]byte, error) {
			hs, err := t.Health(ctx)
			if err != nil {
				return nil, err
			}
			return json.Marshal(HealthOutput{State: int(hs.State), Reason: hs.Reason})
		})
	return Serve(srv)
}

// FlagProvider applies a feature-flag change. A flag plugin implements it and
// passes it to ServeFlagProvider.
type FlagProvider interface {
	ApplyFlag(ctx context.Context, c FlagChange) error
}

// ServeFlagProvider runs a FlagProvider as a Rollops feature-flag plugin,
// exposing the "featureflag" capability with the apply_flag tool.
func ServeFlagProvider(name, version string, p FlagProvider, safety Safety) error {
	m := NewManifest(name, version).
		Capability(CapabilityFeatureFlag, "Feature-flag provider").
		Tool(ToolApplyFlag, "Set a flag's rollout percentage / state", true).
		Done().
		Safety(safety).
		Build()

	srv := NewServer(m).
		HandleTool(CapabilityFeatureFlag, ToolApplyFlag, func(ctx context.Context, in []byte) ([]byte, error) {
			var c FlagChange
			if err := json.Unmarshal(in, &c); err != nil {
				return nil, err
			}
			if err := p.ApplyFlag(ctx, c); err != nil {
				return nil, err
			}
			return []byte("{}"), nil
		})
	return Serve(srv)
}
