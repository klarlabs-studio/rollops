// Package store defines the pluggable persistence boundary for runtime state.
//
// The Store is NOT the source of truth for desired state — Git is. It holds
// observed state, in-flight rollouts, schedules, and history. Backends:
// SQLite (default, single-file, single-binary friendly), Postgres (studio
// scale, shared state), and mnemos (optional bitemporal history).
package store

import (
	"context"
	"time"

	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/pkg/target"
)

// Store persists runtime rollout state behind a single interface so the
// backend (SQLite / Postgres / mnemos) is a deployment choice, not a coupling.
type Store interface {
	// SaveRollout persists a rollout's current statekit state. Called on every
	// transition so an interrupted rollout resumes or rolls back deterministically.
	SaveRollout(ctx context.Context, r rollout.Rollout) error

	// LoadRollout retrieves a rollout by id.
	LoadRollout(ctx context.Context, id string) (rollout.Rollout, error)

	// ObservedState returns the last observed fingerprint for a target,
	// against which the reconciler diffs desired state to detect drift.
	ObservedState(ctx context.Context, targetRef string) (target.Fingerprint, error)

	// Schedule queues a rollout for a future time.
	Schedule(ctx context.Context, s rollout.ScheduledRollout) error

	// DueSchedules returns schedules whose DueAt has passed, fired on each
	// reconcile tick.
	DueSchedules(ctx context.Context, now time.Time) ([]rollout.ScheduledRollout, error)

	// History returns the audit/history records for a target, newest first.
	History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error)
}
