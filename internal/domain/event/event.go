// Package event holds the envelope every durable record of a change is written
// in (spec §16.2) and the vocabulary of what may be recorded (§16.3).
//
// Aggregates are the source of truth; this log is authoritative for what
// happened, not for what is (ADR-0003). It lives in SQLite alongside the
// aggregates so that the state write and the record of it share one transaction
// (ADR-0005). Nothing here knows that: an envelope is a value, and the port it
// travels through is what enforces the transaction.
//
// Two properties are the reason this package exists rather than a struct per
// call site. An event is attributed (INV-005) and redacted (INV-012) by
// construction, so there is no path that produces an anonymous or
// credential-bearing one; and it is append-only (INV-013), so a mistake in it
// is permanent, which is why New validates more than a mutable record would.
//
// The one thing New cannot check is Payload. It is opaque JSON by design —
// adding a field to it is not a schema migration, which is most of its value —
// and so redaction of what goes inside belongs to whoever builds it. The domain
// types that end up there redact their own sensitive values first.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
)

// ErrInvalidEvent reports an envelope that must not be appended.
var ErrInvalidEvent = errors.New("event: invalid event")

// Type names what happened. It is a string rather than a closed enum because
// §16.4 requires consumers to tolerate unknown types: a binary reading a log
// written by a newer one has to be able to carry a type it does not recognise.
// Writing is the strict direction — New refuses a type this binary does not
// know, so a typo is a failing test and not an event nothing will ever match.
type Type string

// The vocabulary of §16.3. The pipeline phase is deliberately absent until
// there is a pipeline; an event type is public surface the moment it is
// persisted, and one written early is one that must be tolerated forever.
const (
	ProjectCreated     Type = "project.created"
	EnvironmentCreated Type = "environment.created"
	ArtifactRegistered Type = "artifact.registered"
	ReleaseCreated     Type = "release.created"

	DeploymentPlanCreated         Type = "deployment.plan.created"
	DeploymentQueued              Type = "deployment.queued"
	DeploymentApprovalRequested   Type = "deployment.approval.requested"
	DeploymentApproved            Type = "deployment.approved"
	DeploymentApprovalRejected    Type = "deployment.approval.rejected"
	DeploymentStarted             Type = "deployment.started"
	DeploymentOperationStarted    Type = "deployment.operation.started"
	DeploymentOperationCompleted  Type = "deployment.operation.completed"
	DeploymentOperationFailed     Type = "deployment.operation.failed"
	DeploymentVerificationStarted Type = "deployment.verification.started"
	DeploymentVerificationDone    Type = "deployment.verification.completed"
	DeploymentPaused              Type = "deployment.paused"
	DeploymentPromotionStarted    Type = "deployment.promotion.started"
	DeploymentPromoted            Type = "deployment.promoted"
	DeploymentSucceeded           Type = "deployment.succeeded"
	DeploymentFailed              Type = "deployment.failed"

	RollbackStarted   Type = "rollback.started"
	RollbackCompleted Type = "rollback.completed"
	RollbackFailed    Type = "rollback.failed"

	DriftDetected    Type = "drift.detected"
	ReconcileStarted Type = "reconcile.started"
	ReconcileDone    Type = "reconcile.completed"
	ReconcileFailed  Type = "reconcile.failed"
	PolicyEvaluated  Type = "policy.evaluated"
	AccessDenied     Type = "security.access_denied"
)

var types = []Type{
	ProjectCreated, EnvironmentCreated, ArtifactRegistered, ReleaseCreated,
	DeploymentPlanCreated, DeploymentQueued,
	DeploymentApprovalRequested, DeploymentApproved, DeploymentApprovalRejected,
	DeploymentStarted, DeploymentOperationStarted, DeploymentOperationCompleted,
	DeploymentOperationFailed, DeploymentVerificationStarted,
	DeploymentVerificationDone, DeploymentPaused, DeploymentPromotionStarted,
	DeploymentPromoted, DeploymentSucceeded, DeploymentFailed,
	RollbackStarted, RollbackCompleted, RollbackFailed,
	DriftDetected, ReconcileStarted, ReconcileDone, ReconcileFailed,
	PolicyEvaluated, AccessDenied,
}

// Types returns the vocabulary this binary can write, in the order §16.3 lists
// it. The slice is a copy: a caller that sorted it would reorder the canon.
func Types() []Type {
	out := make([]Type, len(types))
	copy(out, types)
	return out
}

// Valid reports whether t is a type this binary knows how to write. An unknown
// type read back from storage is not an error — see the note on Type.
func (t Type) Valid() bool {
	for _, known := range types {
		if t == known {
			return true
		}
	}
	return false
}

// AggregateType names what an event is filed against. The timeline is read by
// filtering on it, so it is part of the envelope rather than inferred from the
// event type — `deployment.plan.created` could plausibly belong to either.
type AggregateType string

const (
	AggregateProject         AggregateType = "project"
	AggregateEnvironment     AggregateType = "environment"
	AggregateArtifact        AggregateType = "artifact"
	AggregateRelease         AggregateType = "release"
	AggregateDeployment      AggregateType = "deployment"
	AggregatePlan            AggregateType = "plan"
	AggregateVerificationRun AggregateType = "verification_run"
	AggregatePipelineRun     AggregateType = "pipeline_run"
)

// parseAggregateID checks that id is an identifier of the declared kind. An
// event filed against the wrong kind is invisible to the timeline that should
// show it and misleading on the one that does, and neither failure is loud.
func parseAggregateID(a AggregateType, id string) error {
	var err error
	switch a {
	case AggregateProject:
		_, err = identity.ParseProjectID(id)
	case AggregateEnvironment:
		_, err = identity.ParseEnvironmentID(id)
	case AggregateArtifact:
		_, err = identity.ParseArtifactID(id)
	case AggregateRelease:
		_, err = identity.ParseReleaseID(id)
	case AggregateDeployment:
		_, err = identity.ParseDeploymentID(id)
	case AggregatePlan:
		_, err = identity.ParsePlanID(id)
	case AggregateVerificationRun:
		_, err = identity.ParseVerificationRunID(id)
	case AggregatePipelineRun:
		_, err = identity.ParsePipelineRunID(id)
	default:
		return fmt.Errorf("%w: unknown aggregate type %q", ErrInvalidEvent, a)
	}
	return err
}

// Event is one durable record of a change (§16.2).
type Event struct {
	ID   identity.EventID
	Type Type

	// Version is the schema version of Payload, starting at 1. It is per type:
	// changing the shape of one payload does not renumber the others.
	Version int

	AggregateType AggregateType
	AggregateID   string

	// Sequence is the log's global position, gapless and monotonic across every
	// aggregate (ADR-0003). It is what a cursor pages by, and only the appender
	// can assign it — New leaves it zero and refuses one that is set.
	Sequence uint64

	Time      time.Time
	Principal identity.Principal

	// CorrelationID ties every event of one user intent together across plan,
	// apply and verify (§16.4). An event that names no chain starts one, so this
	// is never empty; CausationID names the event that directly caused this one
	// and is empty at the root of a chain.
	CorrelationID identity.EventID
	CausationID   identity.EventID

	// Payload is opaque JSON. Adding a field to it is not a schema migration,
	// which is most of the reason it is not a typed union — but it also means
	// nothing here can redact it. See the package doc.
	Payload  json.RawMessage
	Metadata map[string]string
}

// New returns an event ready to append: identified, stamped, attributed to by,
// and redacted. It does not append anything — that is the appender's job, and
// the appender is what fails outside a transaction (ADR-0003).
func New(g identity.Generator, c identity.Clock, by identity.Principal, e Event) (Event, error) {
	if err := by.Validate(); err != nil {
		return Event{}, err
	}
	if !e.Type.Valid() {
		return Event{}, fmt.Errorf("%w: unknown type %q", ErrInvalidEvent, e.Type)
	}
	if strings.TrimSpace(e.AggregateID) == "" {
		return Event{}, fmt.Errorf("%w: missing aggregate id", ErrInvalidEvent)
	}
	if err := parseAggregateID(e.AggregateType, e.AggregateID); err != nil {
		return Event{}, fmt.Errorf("%w: aggregate id: %w", ErrInvalidEvent, err)
	}
	if e.Version < 0 {
		return Event{}, fmt.Errorf("%w: negative version %d", ErrInvalidEvent, e.Version)
	}
	if e.Sequence != 0 {
		return Event{}, fmt.Errorf("%w: sequence %d is the appender's to assign", ErrInvalidEvent, e.Sequence)
	}
	if len(e.Payload) > 0 && !json.Valid(e.Payload) {
		return Event{}, fmt.Errorf("%w: payload is not json", ErrInvalidEvent)
	}
	if err := checkRef("correlation id", e.CorrelationID); err != nil {
		return Event{}, err
	}
	if err := checkRef("causation id", e.CausationID); err != nil {
		return Event{}, err
	}

	id, err := identity.NewEventID(g)
	if err != nil {
		return Event{}, err
	}
	e.ID = id
	if e.CorrelationID == "" {
		e.CorrelationID = id
	}
	if e.Version == 0 {
		e.Version = 1
	}
	e.Time = c.Now()
	e.Principal = by.Redacted()
	e.Metadata = identity.RedactSecrets(e.Metadata)
	return e, nil
}

// checkRef accepts an empty reference and rejects a set one that does not name
// an event. Both fields point into this log, so anything else is a reference
// the timeline cannot resolve.
func checkRef(field string, ref identity.EventID) error {
	if ref == "" {
		return nil
	}
	if _, err := identity.ParseEventID(string(ref)); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrInvalidEvent, field, err)
	}
	return nil
}
