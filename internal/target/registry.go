// Package target wires deployment-target plugins: it maps a config target kind
// (ssh, ftp, kubernetes, or a community plugin) to a constructed, bound
// pkg/target.Target. First-party targets register a Factory at init; the gRPC
// plugin escape hatch registers one too. The engine never constructs targets
// directly — it asks the Registry.
package target

import (
	"fmt"
	"sort"

	"go.klarlabs.de/rollops/internal/config"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// Factory constructs a Target bound to one piece of infrastructure from its
// config. Binding happens at construction because the Target contract's
// Observe/Health take no arguments — a Target instance *is* one target.
type Factory func(config.Target) (pt.Target, error)

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
func (r *Registry) Build(t config.Target) (pt.Target, error) {
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
