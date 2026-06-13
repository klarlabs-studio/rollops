// Package trafficrouting couples a rollout's canary weight steps to a real
// traffic router (Gateway API, Istio, NGINX, …) via a "trafficrouter"-capability
// plugin. Where featureflags shifts user-facing exposure at the application
// layer, this shifts live network traffic between a stable and canary backend,
// making a weighted canary mean what Argo Rollouts means by it.
package trafficrouting

import (
	"context"
	"fmt"
)

// Change is one weight shift toward the canary backend.
type Change struct {
	Route         string
	Namespace     string
	StableService string
	CanaryService string
	Weight        int
}

// Router applies a traffic weight shift.
type Router interface {
	SetWeight(ctx context.Context, c Change) error
}

// Hook drives a Router with validation, mirroring featureflags.Hook.
type Hook struct {
	Router Router
}

// Apply validates the change and routes it. A nil Router is a no-op so callers
// need not branch on whether traffic routing is configured.
func (h Hook) Apply(ctx context.Context, c Change) error {
	if h.Router == nil {
		return nil
	}
	if c.Route == "" {
		return fmt.Errorf("trafficrouting: route is required")
	}
	if c.Weight < 0 || c.Weight > 100 {
		return fmt.Errorf("trafficrouting: weight must be between 0 and 100")
	}
	return h.Router.SetWeight(ctx, c)
}
