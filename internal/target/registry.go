// Package target wires deployment-target plugins: it maps a config target kind
// (ssh, ftp, kubernetes, or a community plugin) to a constructed target bound
// to the capabilities the host resolved for it. First-party targets register a
// Factory at init; the gRPC plugin escape hatch registers one too. The engine
// never constructs targets directly — it asks the Registry.
package target

import (
	"context"
	"fmt"
	"sort"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
	"go.klarlabs.de/rollops/pkg/target/v1adapter"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// Factory constructs a target bound to one piece of infrastructure from its
// config. Binding happens at construction because a Target instance *is* one
// target, and the binding is what carries its capabilities: the engine reads
// them from the Bound rather than asserting interfaces on what it holds
// (ADR-0006).
type Factory func(config.Target) (*targetv2.Bound, error)

// FromV1 lifts a target still written against the v1 contract into a v2
// binding, so a target does not have to migrate in the same release the
// contract does (§9.6). The claim comes from the adapter, which derives it from
// what the v1 target actually implements, so there is no separately authorized
// ceiling to narrow against and nothing to refuse — a target compiled into this
// binary was authorized by being compiled in.
func FromV1(kind string, build func(config.Target) (pt.Target, error)) Factory {
	return func(cfg config.Target) (*targetv2.Bound, error) {
		inner, err := build(cfg)
		if err != nil {
			return nil, err
		}
		adapted := v1adapter.New(inner, targetv2.Metadata{Kind: kind, Name: cfg.Ref, Version: "v1"})
		caps, err := adapted.Capabilities(context.Background())
		if err != nil {
			return nil, fmt.Errorf("target: %s %q: capabilities: %w", kind, cfg.Ref, err)
		}
		return targetv2.NewBound(adapted, caps), nil
	}
}

// Registry resolves a config target kind to a constructed Target.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{factories: make(map[string]Factory)}
}

// Register binds a kind to its Factory. Registering a kind twice panics — a
// duplicate target kind is a programming error, caught at startup.
func (r *Registry) Register(kind string, f Factory) {
	if _, dup := r.factories[kind]; dup {
		panic(fmt.Sprintf("target: kind %q already registered", kind))
	}
	r.factories[kind] = f
}

// Build constructs the Target for the given config target, or returns an error
// naming the unknown kind and the kinds that are available.
func (r *Registry) Build(t config.Target) (*targetv2.Bound, error) {
	f, ok := r.factories[t.Kind]
	if !ok {
		return nil, fmt.Errorf("target: unknown kind %q (registered: %v)", t.Kind, r.Kinds())
	}
	return f(t)
}

// Kinds lists the registered kinds, sorted for stable error messages.
func (r *Registry) Kinds() []string {
	out := make([]string, 0, len(r.factories))
	for k := range r.factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
