// Package target defines the public contract every Rollops deployment
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

	// Root is the local filesystem directory that RELATIVE referenced sources
	// resolve against — the directory of the config file this manifest came from
	// (the Git checkout for the daemon, the config file's dir for the CLI). It is
	// ambient execution context, not desired state: it is deliberately excluded
	// from Checksum and never persisted or sent over the wire (json:"-"), so a
	// prior manifest loaded from history re-roots against the current checkout.
	Root string `json:"-"`

	// Rendered holds the concrete manifest bytes produced from a referenced
	// source (manifestFrom) at apply time. It is persisted with the rollout so a
	// later rollback restores EXACTLY what was deployed instead of re-rendering
	// the source — which could differ (or be unavailable) if the referenced
	// Kustomize/Helm/path files changed since, or if the rollback runs where no
	// checkout is at hand (the manual CLI/UI/API path has no Root). Empty for
	// inline manifests and the legacy flat keys, which render deterministically
	// from Spec; omitted from JSON when empty so those manifests are unchanged.
	Rendered []byte `json:"rendered,omitempty"`
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

// Renderer is an OPTIONAL capability: a target whose desired manifest is
// resolved (rendered) from its spec at plan/apply time — e.g. the Kubernetes
// target rendering a Helm chart, a Kustomize overlay, or a referenced file.
// The engine uses it to (1) surface the rendered result in the plan and (2),
// when the manifest is Referenced (resolved from an external source rather than
// an inline manifest), stamp the drift checksum over the RENDERED bytes — so
// edits to the referenced files are detected as drift even under shallow
// verification. Targets whose desired state is verbatim need not implement it.
type Renderer interface {
	// Render resolves desired to concrete manifest bytes.
	Render(ctx context.Context, desired Manifest) ([]byte, error)
	// Referenced reports whether desired resolves from an external source; when
	// false the engine keeps the spec-derived checksum unchanged. Must be cheap
	// (spec inspection, no rendering).
	Referenced(desired Manifest) bool
}

// Reaper is an OPTIONAL capability: a target that can remove the resources it
// owns, identified by the marker rollops applied to them.
//
// It exists for one case the reconcile loop cannot otherwise reach: the
// RolloutConfig itself was deleted (#154). Pruning runs as part of APPLYING a
// target, so it needs the target to still exist — deleting the declaration
// removes the very thing that would clean up after it, and the resources run on
// with nothing in Git describing them.
//
// This is the FIRST destructive capability in this package. Everything else here
// observes. Two consequences worth stating rather than discovering:
//
//   - Implementing it is opt-in for a target KIND, and invoking it is opt-in for
//     a target INSTANCE. The engine never calls this because state drifted; only
//     because the declaration is gone and the operator asked for that to mean
//     removal.
//   - A target that cannot scope a deletion precisely should not implement it.
//     Reaping too little is the bug this fixes; reaping too much is worse.
type Reaper interface {
	// ReapTarget removes the resources this target owns and reports how many
	// were removed. It MUST be scoped to resources this target marked as its
	// own — never "everything in the namespace".
	//
	// Idempotent: reaping an already-reaped target removes nothing and is not
	// an error, because the caller may retry after a partial failure.
	ReapTarget(ctx context.Context) (removed int, err error)
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

// Preflighter is an optional capability: a target that can answer "would this
// apply succeed?" without applying it.
//
// It exists because reconciling each target independently let a batch apply
// half of itself. A repository declared an Ingress and the Traefik Middleware
// its router.middlewares annotation named; the Ingress applied, the Middleware
// failed on RBAC the service account did not hold, and only the Middleware's
// own target rolled back. Traefik could not resolve the dangling reference,
// never built the router, and the apex domain served 404 until the Middleware
// was applied by hand.
//
// Neither target was wrong on its own terms — one applied, one rolled back
// cleanly. The breakage lived in the relationship between them, which nothing
// modelled.
//
// Compensating rollback cannot fix that, because rolling back a CREATE means
// deleting the resource, and rollops deliberately holds no delete verb on the
// types where removal is destructive (PVCs would lose data, CronJobs would
// silently drop scheduled work, a Middleware is often the only thing enforcing
// a security control). So the batch has to fail BEFORE it applies anything
// rather than unwind afterwards, and that requires asking each target whether
// it would succeed.
//
// A target that cannot answer simply does not implement this: the caller then
// learns nothing about it and proceeds as before, which is strictly the
// current behaviour.
type Preflighter interface {
	// Preflight reports whether Apply(desired) would succeed, WITHOUT changing
	// anything. A nil error means "as far as can be told, yes".
	//
	// It must not mutate the target. An implementation that cannot check
	// without mutating must not implement this interface at all — a preflight
	// with side effects is worse than none, because the batch it was meant to
	// protect has already been half-applied by the check.
	Preflight(ctx context.Context, desired Manifest) error
}
