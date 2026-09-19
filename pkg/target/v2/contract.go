package targetv2

import "context"

// Target is the mandatory contract (§9.2). Every method is required; a target
// that cannot do one of them returns ErrUnsupported naming the capability,
// because a method that is simply absent cannot be observed from the other
// side of a process boundary.
//
// Callers should hold a *Bound rather than a Target: it is what ties a target
// to the capabilities the host resolved for it, and the optional capabilities
// are reachable only through it.
type Target interface {
	// Metadata identifies the implementation and the bound instance. It is
	// local: no call, no error, nothing that can fail.
	Metadata() Metadata

	// Capabilities reports what this target can do here and now (§9.3). It
	// takes a context because the honest answer may need the substrate — a
	// Kubernetes target cannot know it lacks progressive delivery until it
	// has looked for the CRD.
	Capabilities(context.Context) (Capabilities, error)

	// Inspect reports what is live: the inventory the target manages and a
	// fingerprint of it. It does not change anything.
	Inspect(context.Context, InspectRequest) (ObservedState, error)

	// Plan reports what Apply would do, without doing it. It MUST be free of
	// side effects — §9.5 makes that a conformance axis, because a plan with
	// side effects has already half-applied the batch it was meant to guard.
	Plan(context.Context, PlanRequest) (PlanResult, error)

	// Apply converges the target on the desired state. It MUST honour the
	// idempotency key (§9.4): the same key replayed returns the same semantic
	// result, or IdempotencyConflict when the target cannot promise that.
	Apply(context.Context, ApplyRequest) (ApplyResult, error)

	// Observe reports how a particular apply is going — progress and health,
	// as opposed to Inspect's inventory.
	Observe(context.Context, ObserveRequest) (Observation, error)

	// Rollback undoes an apply using the substrate's own mechanism. A target
	// without CapabilityNativeRollback returns ErrUnsupported and the engine
	// rolls back by applying the previous desired state instead.
	Rollback(context.Context, RollbackRequest) (RollbackResult, error)
}

// Promoter is optional (CapabilityProgressiveDelivery): advance a partially
// rolled-out apply to completion.
type Promoter interface {
	Promote(context.Context, PromoteRequest) (PromoteResult, error)
}

// Drifter is optional (CapabilityDriftDetection): compare live state against
// desired natively, rather than by fingerprint comparison.
type Drifter interface {
	DetectDrift(context.Context, DriftRequest) (DriftResult, error)
}

// Pruner is optional (CapabilityPrune): remove the resources this target owns.
// It is the only destructive method in this package. It MUST be scoped to
// resources the target marked as its own — pruning too little is the problem
// it solves, pruning too much is worse.
type Pruner interface {
	Prune(context.Context, PruneRequest) (PruneResult, error)
}

// Metadata identifies a target for attribution (INV-005). Version is the
// implementation's, not the deployment's: it is what an audit reads to explain
// why two applies of the same desired state behaved differently.
type Metadata struct {
	Kind    string // implementation, e.g. "kubernetes"
	Name    string // bound instance, e.g. "x/prod/api"
	Version string // implementation version
}

// DesiredState is what the target should converge on. Spec is opaque to
// everything but the target that declared Kind, which is what keeps the engine
// ignorant of any particular substrate (INV-007).
type DesiredState struct {
	Kind     string
	Spec     []byte
	Checksum string
	Labels   map[string]string
}

// InspectRequest has no fields yet. It exists so that Inspect can grow one
// without changing its signature or the wire — the reason RPC contracts take
// a request message even when there is nothing to say.
type InspectRequest struct{}

// ObservedState is the live inventory. Meta is diagnostic detail and never
// carries secrets (INV-012).
type ObservedState struct {
	Fingerprint string
	Resources   []Resource
	Meta        map[string]string
}

// Resource is one live object. Parent is the owning resource's name, empty for
// a top-level object, so a UI can render the ownership tree.
type Resource struct {
	Kind      string
	Name      string
	Namespace string
	Status    string
	Parent    string
}

// PlanRequest asks what applying Desired would do.
type PlanRequest struct {
	Desired DesiredState
}

// PlanResult is what Apply would do. Blockers names the reasons Apply would
// fail; a non-empty Blockers means the caller must not apply. Empty means
// nothing is known to block it, which is also what a target that cannot
// check says — it does not claim the apply will succeed.
type PlanResult struct {
	Changes  bool
	Diff     string
	Rendered []byte
	Blockers []string

	// RenderedChecksum identifies the bytes in Rendered, and is set only when
	// the desired checksum does not. A desired state that points at an external
	// source — a Helm chart, a kustomization, a path — is checksummed over the
	// pointer, and editing the files behind it leaves that checksum untouched.
	// The host records this one instead, so drift is measured against what was
	// deployed rather than against what it was asked for.
	RenderedChecksum string
}

// ApplyRequest converges on Desired. IdempotencyKey is mandatory and is minted
// by the host from durable state; see IdempotencyKeyFor.
type ApplyRequest struct {
	Desired        DesiredState
	IdempotencyKey string
}

// ApplyResult reports what happened. Handle is opaque and target-defined: it
// names this apply to a later Observe, Promote or Rollback, and it is empty
// for a target with nothing to name.
type ApplyResult struct {
	Changed bool
	Detail  string
	Handle  string
}

// ObserveRequest watches one apply. An empty Handle asks about the target's
// current state, which is what a target that mints no handles always answers.
type ObserveRequest struct {
	Handle string
}

// Observation is how an apply is going.
type Observation struct {
	Fingerprint string
	Health      HealthStatus
	Meta        map[string]string
}

// HealthState is the coarse readiness verdict that feeds verification and
// auto-rollback.
type HealthState int

const (
	HealthUnknown HealthState = iota
	HealthHealthy
	HealthDegraded
	HealthUnhealthy
)

// HealthStatus carries the verdict and a reason for the timeline. A target
// without CapabilityHealthObservation reports HealthUnknown rather than
// guessing, which is why the capability exists separately from the field.
type HealthStatus struct {
	State  HealthState
	Reason string
}

// RollbackRequest undoes the apply named by Handle. There is no "previous
// state" field: a target that needs to be told what to return to does not have
// native rollback, and the engine applies the previous desired state itself.
type RollbackRequest struct {
	Handle string
}

// RollbackResult reports what the rollback did.
type RollbackResult struct {
	Changed bool
	Detail  string
}

// PromoteRequest advances the apply named by Handle.
type PromoteRequest struct {
	Handle string
}

// PromoteResult reports what the promotion did.
type PromoteResult struct {
	Detail string
}

// DriftRequest compares live state against Desired.
type DriftRequest struct {
	Desired DesiredState
}

// DriftResult reports whether live state has moved away from desired.
type DriftResult struct {
	Drifted bool
	Detail  string
}

// PruneRequest has no fields yet, for the same reason as InspectRequest.
type PruneRequest struct{}

// PruneResult reports how many owned resources were removed. Pruning an
// already-pruned target removes nothing and is not an error, so that a caller
// may retry after a partial failure.
type PruneResult struct {
	Removed int
}
