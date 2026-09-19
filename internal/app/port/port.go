// Package port declares the interfaces the application layer depends on and
// the storage layer satisfies.
//
// Nothing here mentions SQL, HTTP or any provider: a port that named its
// implementation would make the use cases untestable without it and would tie
// the domain to one transport (INV-006). The interfaces are deliberately
// narrow and one per aggregate rather than a single Store, so that a use case
// declares the two repositories it touches instead of depending on all of them
// (spec 17.1).
package port

import (
	"context"
	"errors"
	"time"

	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/release"
)

var (
	// ErrNotFound reports that no aggregate has the requested identity.
	ErrNotFound = errors.New("not found")

	// ErrAlreadyExists reports a write that would duplicate something the
	// domain requires to be unique — a project name, a release version, or an
	// artifact already registered under the same digest (INV-002).
	ErrAlreadyExists = errors.New("already exists")

	// ErrRevisionConflict reports that the aggregate changed between the read
	// and the write. It is distinct from ErrNotFound because the caller's
	// recourse differs: a conflict is worth retrying from a fresh read, a
	// missing aggregate is not.
	ErrRevisionConflict = errors.New("revision conflict")

	// ErrNoTransaction reports an event appended outside a transaction. It is
	// the enforcement of ADR-0003: an event whose aggregate write was rolled
	// back is a lie in the timeline, so the append fails loudly at the first
	// test that forgets the transaction rather than producing one.
	ErrNoTransaction = errors.New("no transaction")
)

// Transactor runs a unit of work atomically. The application layer says "these
// writes are one unit" and never sees the transaction itself; an implementation
// carries it on the derived context, which is what keeps database/sql out of
// every repository signature (ADR-0003).
//
// Nesting reuses the open transaction rather than opening a savepoint: partial
// rollback of half a command is not a behaviour any use case has asked for.
type Transactor interface {
	WithinTransaction(ctx context.Context, fn func(context.Context) error) error
}

// ProjectRepository persists projects.
type ProjectRepository interface {
	// Create stores a new project. It returns ErrAlreadyExists if the name is
	// taken, because a name is how a project is addressed on every surface.
	Create(ctx context.Context, p project.Project) error

	// Update stores a change to an existing project and returns the revision it
	// committed at. It returns ErrRevisionConflict if p.Revision is not the
	// stored one — including when it is zero, which means the caller did not
	// read the project first.
	Update(ctx context.Context, p project.Project) (identity.Revision, error)

	Get(ctx context.Context, id identity.ProjectID) (project.Project, error)
	GetByName(ctx context.Context, name string) (project.Project, error)
	List(ctx context.Context) ([]project.Project, error)
}

// EnvironmentRepository persists environments and their target bindings.
type EnvironmentRepository interface {
	// Create stores a new environment. Names are unique within a project, not
	// globally: two projects may each have a "production".
	Create(ctx context.Context, e environment.Environment) error
	Update(ctx context.Context, e environment.Environment) (identity.Revision, error)

	Get(ctx context.Context, id identity.EnvironmentID) (environment.Environment, error)
	GetByName(ctx context.Context, p identity.ProjectID, name string) (environment.Environment, error)
	List(ctx context.Context, p identity.ProjectID) ([]environment.Environment, error)
}

// ArtifactRepository persists artifact metadata — never artifact content. What
// is stored is where the bytes are and what they must hash to (ADR-0004).
//
// There is no Update: an artifact is immutable (INV-002), so the absence of the
// method is the enforcement.
type ArtifactRepository interface {
	// Create registers an artifact. Registering the same digest twice within a
	// project returns ErrAlreadyExists rather than a second row: it is the same
	// artifact, and content is what identifies it.
	Create(ctx context.Context, a artifact.Artifact) error

	Get(ctx context.Context, id identity.ArtifactID) (artifact.Artifact, error)
	GetByDigest(ctx context.Context, p identity.ProjectID, k artifact.Kind, d string) (artifact.Artifact, error)
	List(ctx context.Context, p identity.ProjectID) ([]artifact.Artifact, error)
}

// ReleaseRepository persists releases. Like an artifact a release is immutable
// (INV-003), so there is no Update.
type ReleaseRepository interface {
	// Create stores a release and the roles binding its artifacts. It returns
	// ErrNotFound if an artifact it names is not registered: a release that
	// referred to nothing would be undeployable at exactly the wrong moment.
	Create(ctx context.Context, r release.Release) error

	Get(ctx context.Context, id identity.ReleaseID) (release.Release, error)
	GetByVersion(ctx context.Context, p identity.ProjectID, version string) (release.Release, error)
	List(ctx context.Context, p identity.ProjectID) ([]release.Release, error)

	// FindByFingerprint answers "have we already built this". The fingerprint
	// is recomputed from the stored release rather than trusted, so a row whose
	// recorded fingerprint disagrees with its content is not returned.
	FindByFingerprint(ctx context.Context, p identity.ProjectID, f string) ([]release.Release, error)
}

// PlanRepository persists deployment plans.
//
// There is no Update. A plan is what was reviewed and approved, and the whole
// point of hashing it is that the stored copy cannot change; a method that
// wrote over one would be the tamper path the hash exists to detect.
type PlanRepository interface {
	Create(ctx context.Context, p plan.DeploymentPlan) error

	// Get returns the stored plan. It does not verify the hash — that is the
	// caller's decision, because a plan is also fetched to be explained, and
	// refusing to show a tampered plan would hide the evidence.
	Get(ctx context.Context, id identity.PlanID) (plan.DeploymentPlan, error)
}

// ApprovalRepository persists approvals.
//
// There is no Update and no Delete. An approval is a statement somebody made
// about a revision at a moment; editing one would rewrite what they said, and
// withdrawing one is a new approval recording the withdrawal rather than the
// disappearance of the old. This is the same reason PlanRepository has no
// Update — both are records apply reads to decide whether to proceed.
type ApprovalRepository interface {
	Create(ctx context.Context, a policy.Approval) error

	// ListForSubject returns every approval recorded against the subject,
	// oldest first. It takes the kind and id rather than a full SubjectRef
	// because the caller is asking what has been said about this subject across
	// all its revisions — filtering to the one under apply is the domain's job
	// (policy.Decision.SatisfiedBy), and doing it here would hide from an
	// operator that an earlier revision was approved and then re-planned.
	ListForSubject(ctx context.Context, kind, id string) ([]policy.Approval, error)
}

// DeploymentRepository persists deployments. Unlike a release a deployment
// changes as it runs, so it has both an Update and a revision to lose a race on.
type DeploymentRepository interface {
	// Create stores a new deployment and returns the revision it committed at.
	// It returns the revision for the same reason Update does: the caller's copy
	// is the one that goes on to be transitioned, and a copy carrying a revision
	// the store never agreed to would lose the very next compare-and-set.
	Create(ctx context.Context, d deployment.Deployment) (identity.Revision, error)

	// Update stores a change and returns the revision it committed at. It
	// returns ErrRevisionConflict if d.Revision is not the stored one, which is
	// what keeps two workers from advancing one deployment past each other.
	Update(ctx context.Context, d deployment.Deployment) (identity.Revision, error)

	Get(ctx context.Context, id identity.DeploymentID) (deployment.Deployment, error)

	// ListForEnvironment returns an environment's deployments, most recent
	// first. Ordering is part of the contract because the first result is how a
	// caller asks what is currently deployed.
	ListForEnvironment(ctx context.Context, e identity.EnvironmentID) ([]deployment.Deployment, error)

	// FindActive returns the environment's deployment that has not reached a
	// terminal status, if there is one. It returns ErrNotFound when the
	// environment is idle — an answer, not a failure, and the caller branches
	// on it to decide whether a new deployment may start.
	FindActive(ctx context.Context, e identity.EnvironmentID) (deployment.Deployment, error)
}

// Page selects a slice of the event log. Every read takes one because §24 asks
// for pagination from the start: a timeline that returns everything is one that
// stops working on the estate it was built for, and adding a limit later means
// changing a response shape clients already depend on.
type Page struct {
	// After is an exclusive cursor — the sequence of the last event already
	// seen. Zero means the beginning, which is unambiguous because sequences
	// start at one. Paging by sequence rather than by offset is what makes a
	// page stable while the log is being appended to (ADR-0003).
	After uint64

	// Limit caps the page. Zero or less means DefaultPageSize, and anything
	// above MaxPageSize is clamped to it rather than refused: a caller asking
	// for too much wants as much as it can have.
	Limit int
}

// Page size bounds. They live here rather than in each store so that the two
// implementations cannot disagree about what an unset limit means.
const (
	DefaultPageSize = 100
	MaxPageSize     = 1000
)

// Normalized applies the limit bounds. Every implementation calls it, so the
// conformance suite can assert the behaviour once.
func (p Page) Normalized() Page {
	switch {
	case p.Limit <= 0:
		p.Limit = DefaultPageSize
	case p.Limit > MaxPageSize:
		p.Limit = MaxPageSize
	}
	return p
}

// EventAppender writes to the domain event log.
//
// It declares one method. There is no update and no delete, so INV-013 is
// enforced by the absence of a call site rather than by a rule someone has to
// remember (ADR-0005) — a rewritten event is the one thing a timeline cannot
// survive.
type EventAppender interface {
	// Append stores e and returns it carrying the Sequence the log assigned.
	// The returned copy is the one to keep: a caller that held onto its own
	// would have an event with no position in the log.
	//
	// It returns ErrNoTransaction when called outside one. A record of a change
	// that did not happen, or a change with no record, is worse than a failed
	// command (ADR-0003).
	Append(ctx context.Context, e event.Event) (event.Event, error)
}

// EventReader reads the log back. It is separate from the appender because the
// two are used by different things — commands append, the API and projections
// read — and a reader handed to a query has no method that could write.
//
// Neither method validates the event type it returns. §16.4 requires consumers
// to tolerate unknown types, and a binary that refused to hand back a row
// written by a newer one would turn a downgrade into data loss.
type EventReader interface {
	// ForAggregate returns one aggregate's events in sequence order, oldest
	// first. This is what GET /v2/deployments/{id}/events reads.
	ForAggregate(ctx context.Context, a event.AggregateType, id string, p Page) ([]event.Event, error)

	// Timeline returns events across every aggregate in sequence order. The
	// global sequence is what makes this pageable without gaps or repeats.
	Timeline(ctx context.Context, p Page) ([]event.Event, error)

	// ForCorrelation returns every event of one user intent, in sequence order.
	// It is the read §16.4 exists for: what a single plan/apply/verify did, as
	// one story, across the several aggregates it touched.
	ForCorrelation(ctx context.Context, c identity.EventID, p Page) ([]event.Event, error)
}

// IdempotencyRecord is the answer a mutation already gave, kept so a retry of
// the same request returns it rather than doing the work a second time (spec
// §18.3).
//
// It does not hold the response. §23.3 has a mutation return identity and let
// the caller read the rest, so the identity is the whole of what a replay owes
// them — and a stored response body would go stale the moment the resource it
// described moved on.
type IdempotencyRecord struct {
	// Operation scopes the key. Keys are the caller's to invent, and a CLI
	// that generates one per invocation would otherwise collide across two
	// unrelated mutations that happened to be given the same one.
	Operation string
	Key       string

	// Fingerprint is a digest of the request the key was first used for. A key
	// reused for a different request is not a retry, and replaying the first
	// answer would tell the caller their second request succeeded.
	Fingerprint digest.Digest

	// Result is the identity the first call returned, and is empty while that
	// call is still running. The claim is written before the work rather than
	// after it: a record written afterwards leaves a window in which two
	// retries both find nothing and both do the work, and that window is
	// exactly the case a key exists for — a client whose first request timed
	// out retrying while the first is still in flight.
	Result string

	CreatedAt time.Time

	// ExpiresAt bounds how long a retry can still be recognised. It is stored
	// rather than derived so that a record keeps the window it was written
	// under — changing the default must not retroactively revive keys that had
	// already lapsed.
	ExpiresAt time.Time
}

// IdempotencyRepository remembers what a mutation already answered.
//
// A record is written in two steps — claimed, then completed — and those are
// the only two writes there are. Nothing edits a completed record: it states
// what one call returned, and editing it would make a replay answer for a
// request that never happened.
//
// Expiry is the caller's to judge against its own clock. The repository stores
// the window and does not enforce it, so a clock skew shows up as a stale
// record the caller can refuse rather than as rows that silently stop
// matching.
type IdempotencyRepository interface {
	// Create claims the key, with r.Result empty. It returns ErrAlreadyExists
	// if the key is taken within its operation, which is how two concurrent
	// retries settle: the loser reads the winner's record rather than doing
	// the work again.
	Create(ctx context.Context, r IdempotencyRecord) error

	// Complete records what the claimed call returned. It returns ErrNotFound
	// if the key was never claimed, and ErrAlreadyExists if it was already
	// completed — a second completion means two calls ran under one claim,
	// which is the thing the claim exists to prevent, so it is reported rather
	// than absorbed.
	Complete(ctx context.Context, operation, key, result string) error

	// Get returns the record for a key within an operation, or ErrNotFound if
	// the key has not been used.
	Get(ctx context.Context, operation, key string) (IdempotencyRecord, error)
}

// EventLog is both halves, which is what a store implements and what the
// composition root wires. It does not undo the split above: what matters is
// that a consumer declares the half it needs, and a query handed an EventReader
// still has no method that could write.
type EventLog interface {
	EventAppender
	EventReader
}
