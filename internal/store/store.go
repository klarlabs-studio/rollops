// Package store defines the pluggable persistence boundary for runtime state.
//
// The Store is NOT the source of truth for desired state — Git is. It holds
// observed state, in-flight rollouts, schedules, and history. Backends:
// SQLite (default, single-file, single-binary friendly), Postgres (studio
// scale, shared state), and mnemos (optional bitemporal history).
package store

import (
	"context"
	"errors"
	"time"

	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/pkg/target"
)

// ErrNotFound is returned by lookups when the requested record does not exist.
var ErrNotFound = errors.New("store: not found")

// Store persists runtime rollout state behind a single interface so the
// backend (SQLite / Postgres / mnemos) is a deployment choice, not a coupling.
type Store interface {
	// SaveRollout persists a rollout's current statekit state. Called on every
	// transition so an interrupted rollout resumes or rolls back deterministically.
	SaveRollout(ctx context.Context, r rollout.Rollout) error

	// LoadRollout retrieves a rollout by id.
	LoadRollout(ctx context.Context, id string) (rollout.Rollout, error)

	// ListRollouts returns the most recent rollouts, newest first (limit<=0 → all).
	ListRollouts(ctx context.Context, limit int) ([]rollout.Rollout, error)

	// SaveObservedState records the fingerprint the reconciler observed for a
	// target, upserting by target ref. Observed state is runtime truth; desired
	// state always comes from Git, never the Store.
	SaveObservedState(ctx context.Context, s rollout.TargetState) error

	// ObservedState returns the last observed fingerprint for a target,
	// against which the reconciler diffs desired state to detect drift.
	// Returns ErrNotFound if the target has never been observed.
	ObservedState(ctx context.Context, targetRef string) (target.Fingerprint, error)

	// ObservedFingerprints returns the last observed fingerprint value for
	// every target in one query, keyed by target ref. Targets never observed
	// are absent from the map. It backs whole-fleet drift reporting without a
	// per-target round trip.
	ObservedFingerprints(ctx context.Context) (map[string]string, error)

	// Schedule queues a rollout for a future time.
	Schedule(ctx context.Context, s rollout.ScheduledRollout) error

	// DueSchedules returns schedules whose DueAt has passed, fired on each
	// reconcile tick.
	DueSchedules(ctx context.Context, now time.Time) ([]rollout.ScheduledRollout, error)

	// DeleteSchedule removes a schedule once fired, so it does not re-fire.
	DeleteSchedule(ctx context.Context, id string) error

	// History returns the audit/history records for a target, newest first.
	History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error)

	// SaveFreeze persists the emergency kill-switch. A restart must restore
	// this row — an in-memory freeze that dies with the process is not a freeze.
	SaveFreeze(ctx context.Context, f FreezeState) error

	// LoadFreeze returns the persisted kill-switch. Missing row is inactive
	// (zero value), not ErrNotFound, so boot can restore unconditionally.
	LoadFreeze(ctx context.Context) (FreezeState, error)
}

// FreezeState is the durable emergency kill-switch. Git does not hold this —
// it is operator runtime state, like an in-flight rollout.
type FreezeState struct {
	Active bool
	Reason string
	By     rollout.Identity
	At     time.Time
}

// LeaseStore is an optional runtime coordination extension for stores that can
// provide cross-process leases. It is deliberately separate from Store so simple
// backends can remain lean while SQLite/Postgres deployments can coordinate
// multiple rollopsd instances.
type LeaseStore interface {
	AcquireLease(ctx context.Context, key, owner string, ttl time.Duration, now time.Time) (bool, error)
	ReleaseLease(ctx context.Context, key, owner string) error
}
