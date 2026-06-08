// Package kubernetes is the first-party Kubernetes target — a "rich" target
// that queries live cluster state rather than a stamped file. It records the
// deployed checksum as an annotation on the managed resource and reads it back
// from the live cluster on Observe, so drift reflects the actual cluster, not a
// local marker.
//
// To honour the core constraint "no Kubernetes dependency", this target drives
// the cluster through the external kubectl binary via the Cluster interface —
// client-go is never compiled into the Rolloffs core. The logic is testable
// with an in-memory fake cluster.
package kubernetes

import (
	"context"
	"fmt"

	"go.klarlabs.de/rolloffs/internal/config"
	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// ChecksumAnnotation is where the deployed checksum is recorded on the resource.
const ChecksumAnnotation = "rolloffs.klarlabs.de/checksum"

// Cluster abstracts the live cluster operations the target needs.
type Cluster interface {
	// Apply applies the manifest and records checksum as the resource annotation.
	Apply(ctx context.Context, manifest []byte, checksum string) error
	// LiveChecksum reads the recorded checksum from the live cluster (empty if
	// the resource is absent or unmanaged).
	LiveChecksum(ctx context.Context) (string, error)
	// Healthy reports rollout readiness.
	Healthy(ctx context.Context) (bool, string, error)
}

// Target deploys to a Kubernetes cluster through a Cluster.
type Target struct {
	cl Cluster
}

// New constructs the real kubectl-backed target from config.
func New(cfg config.Target) (pt.Target, error) {
	s := spec(cfg.Spec)
	if s.str("resource") == "" {
		return nil, fmt.Errorf("kubernetes: target %q: spec.resource is required (e.g. deployment/api)", cfg.Ref)
	}
	return &Target{cl: newKubectl(s)}, nil
}

func newWith(cl Cluster) *Target { return &Target{cl: cl} }

// Apply applies the manifest if the live checksum differs. Idempotent.
func (t *Target) Apply(ctx context.Context, m pt.Manifest) (pt.Result, error) {
	if cur, _ := t.cl.LiveChecksum(ctx); cur == m.Checksum && m.Checksum != "" {
		return pt.Result{Changed: false, Detail: "cluster already at desired checksum"}, nil
	}
	if err := t.cl.Apply(ctx, m.Spec, m.Checksum); err != nil {
		return pt.Result{}, fmt.Errorf("kubernetes: apply: %w", err)
	}
	return pt.Result{Changed: true, Detail: "applied to cluster"}, nil
}

// Observe reads the live checksum annotation — the rich drift signal.
func (t *Target) Observe(ctx context.Context) (pt.Fingerprint, error) {
	cur, err := t.cl.LiveChecksum(ctx)
	if err != nil {
		return pt.Fingerprint{}, fmt.Errorf("kubernetes: observe: %w", err)
	}
	return pt.Fingerprint{Value: cur}, nil
}

// Health reports rollout readiness.
func (t *Target) Health(ctx context.Context) (pt.HealthStatus, error) {
	ok, reason, err := t.cl.Healthy(ctx)
	if err != nil {
		return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: err.Error()}, nil
	}
	if !ok {
		return pt.HealthStatus{State: pt.HealthUnhealthy, Reason: reason}, nil
	}
	return pt.HealthStatus{State: pt.HealthHealthy}, nil
}

type spec map[string]any

func (s spec) str(key string) string {
	if v, ok := s[key].(string); ok {
		return v
	}
	return ""
}
