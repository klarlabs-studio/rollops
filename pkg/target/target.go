// Package target defines the public contract every Rolloffs deployment
// target must satisfy — first-party (Kubernetes, SSH/VM, FTP) and
// community gRPC plugins alike. It is the heart of "infrastructure-agnostic":
// a single Go contract serving both rich targets that query live state and
// dumb targets that verify against a stamped manifest/checksum.
//
// Every implementation MUST pass the conformance suite (pkg/conformance),
// which proves idempotency, fingerprint stability, and health semantics.
package target

import "context"

// Target moves a piece of infrastructure toward a desired state and reports
// what it observes. Implementations are wrapped by the engine in fortify
// (retry, circuit-breaker, rate-limit) and executed through axi-go, so a
// Target itself need not implement resilience or sandboxing — only the
// three semantic operations below.
type Target interface {
	// Apply moves the target toward the desired state.
	//
	// MUST be idempotent: re-running with the same desired state is a no-op.
	// The reconciler may call it repeatedly. Apply does not roll back; the
	// engine drives rollback by Applying a prior Manifest.
	Apply(ctx context.Context, desired Manifest) (Result, error)

	// Observe returns a normalized fingerprint of the target's current state.
	// Rich targets query natively (e.g. kube-apiserver); dumb targets verify
	// against a manifest/checksum stamped at deploy time. The fingerprint MUST
	// be stable: identical state yields an identical fingerprint across calls.
	Observe(ctx context.Context) (Fingerprint, error)

	// Health reports liveness/readiness, consumed by the verify phase and the
	// auto-rollback signal. Observability-free by design (HTTP/TCP/command).
	Health(ctx context.Context) (HealthStatus, error)
}

// Manifest is the desired state for a single target, reconstructed from Git.
// Kind selects the target implementation; Spec is the opaque, target-specific
// payload (validated against the published schema before it reaches here).
type Manifest struct {
	Kind     string            // e.g. "kubernetes", "ssh", "ftp"
	Spec     []byte            // target-specific desired state (already schema-valid)
	Labels   map[string]string // operator metadata, used for audit attribution
	Checksum string            // content hash stamped for dumb-target drift checks
}

// Result reports what an Apply did, for audit and plan/diff surfacing.
type Result struct {
	Changed bool   // false when Apply was a no-op (already at desired state)
	Detail  string // human/agent-readable summary of what changed
}

// Fingerprint is an opaque, comparable snapshot of observed state.
// Drift is defined as desired fingerprint != observed fingerprint.
type Fingerprint struct {
	Value string            // stable hash/marker of observed state
	Meta  map[string]string // optional diagnostic detail (never secrets)
}

// HealthState is the coarse readiness verdict feeding verify + auto-rollback.
type HealthState int

const (
	HealthUnknown HealthState = iota
	HealthHealthy
	HealthDegraded
	HealthUnhealthy
)

// HealthStatus carries the verdict plus a reason for the audit trail.
type HealthStatus struct {
	State  HealthState
	Reason string
}

// Differ is an OPTIONAL capability: a target that can show the diff between the
// desired state and what is live (e.g. `kubectl diff`). The engine surfaces it
// in the plan and the UI; targets that cannot diff simply do not implement it.
type Differ interface {
	Diff(ctx context.Context, desired Manifest) (string, error)
}

// Inspector is an OPTIONAL capability: a target that can list the live resources
// it manages and their status (e.g. `kubectl get`). Surfaced in the UI.
type Inspector interface {
	Resources(ctx context.Context) ([]Resource, error)
}

// Resource is one live object a target manages. Parent (the name of the owning
// resource, empty for a top-level object) lets the UI render an ownership tree
// — Deployment → Pods, etc.
type Resource struct {
	Kind      string
	Name      string
	Namespace string
	Status    string // ready/healthy summary, target-defined
	Parent    string // owner resource name; empty = root
}
