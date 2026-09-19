// Package targets resolves an environment's target bindings to live targets.
//
// It is the bridge between the v2 environment model, where a binding names a
// driver and configures it with references, and the target registry, which
// builds targets from a config.Target. Keeping the bridge here rather than in
// the planner is what lets the planner stay ignorant of configuration: it asks
// for a target and gets one.
//
// Resolved secret material exists only inside the config.Target handed to a
// factory. It is not stored, not returned and not logged — an error naming a
// secret names its key, never its value (INV-012).
package targets

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/engine/planner"
	itarget "go.klarlabs.de/rollops/internal/target"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// Resolver builds targets from bindings.
type Resolver struct {
	registry *itarget.Registry
	secrets  value.Resolver
}

// New returns a resolver. The secret resolver may be nil — a binding
// configured entirely with literals needs none, and requiring one would mean
// standing up a secret provider to deploy to a laptop. A binding that does
// reference a secret is then refused with value.ErrNoResolver, which says what
// is missing rather than handing the target an empty string.
func New(registry *itarget.Registry, secrets value.Resolver) (*Resolver, error) {
	if registry == nil {
		return nil, errors.New("targets: no registry; nothing could be built")
	}
	return &Resolver{registry: registry, secrets: secrets}, nil
}

// Resolve builds the target a binding names.
//
// The caller owns the result and must Close it. Nothing is cached: a target is
// a live connection or a plugin subprocess, and holding one open between plans
// would keep a credential resident for a binding that may have been edited
// since.
func (r *Resolver) Resolve(_ context.Context, req planner.TargetRequest) (*targetv2.Bound, error) {
	cfg, err := r.translate(req.Environment, req.Binding)
	if err != nil {
		return nil, err
	}
	bound, err := r.registry.Build(cfg)
	if err != nil {
		return nil, fmt.Errorf("targets: %s: %w", req.Binding, err)
	}
	return bound, nil
}

// translate turns a binding into the config a factory understands.
func (r *Resolver) translate(e environment.Environment, b environment.TargetBinding) (config.Target, error) {
	spec := make(map[string]any, len(b.Config))
	// Sorted so that a binding with two unresolvable secrets reports the same
	// one every time, rather than whichever the map happened to yield first.
	for _, key := range slices.Sorted(maps.Keys(b.Config)) {
		v, err := b.Config[key].Resolve(r.secrets)
		if err != nil {
			return config.Target{}, fmt.Errorf("targets: %s: config %q: %w", b, key, err)
		}
		spec[key] = v
	}
	return config.Target{
		Kind: b.Driver,
		// Ref is the stable identity a target reports itself by, and it has to
		// distinguish two bindings to the same driver in the same environment.
		// The environment and binding names do that, and unlike the ids they
		// are what an operator sees in a plan.
		Ref: e.Name + "/" + b.Name,
		Env: string(e.Kind),
		// Criticality is deliberately unset. v2 scores risk from the plan
		// (internal/policy/risk) and feeds it to policy as one input; a
		// per-target label here would be a second assessment that could
		// disagree with the first, which is the parallel authorization system
		// spec 12.4 rules out.
		Spec: spec,
	}, nil
}
