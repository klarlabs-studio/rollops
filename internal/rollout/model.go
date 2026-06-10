// Package rollout holds the core runtime entities of a rollout — the state
// the engine drives through the statekit lifecycle and persists via the Store.
//
// Git holds *desired* state; these entities are *runtime* state (in-flight
// rollouts, observed fingerprints, schedules, history). Desired state is
// always reconstructable from Git, so losing the Store degrades history but
// never corrupts what should be deployed.
package rollout

import (
	"time"

	"go.klarlabs.de/rollops/pkg/target"
)

// Phase is the statekit lifecycle state of a rollout.
//
//	pending → validating → [risk gate] → deploying → verifying → promoted
//	                           │                                    │
//	                           ▼                                    ▼
//	                     awaiting-approval                     rolled-back
type Phase string

const (
	PhasePending          Phase = "pending"
	PhaseValidating       Phase = "validating"
	PhaseAwaitingApproval Phase = "awaiting-approval"
	PhaseDeploying        Phase = "deploying"
	PhasePaused           Phase = "paused"
	PhaseVerifying        Phase = "verifying"
	PhasePromoted         Phase = "promoted"
	PhaseRolledBack       Phase = "rolled-back"
)

// Strategy is the progressive-delivery shape of a rollout.
type Strategy string

const (
	StrategyRolling   Strategy = "rolling"
	StrategyCanary    Strategy = "canary"
	StrategyBlueGreen Strategy = "blue-green"
)

// Rollout is a single in-flight or completed deployment of one target.
type Rollout struct {
	ID        string
	TargetRef string          // stable identity of the target being deployed
	Phase     Phase           // current statekit state
	Strategy  Strategy        // progressive-delivery strategy
	Desired   target.Manifest // desired state pulled from Git
	RiskScore float64         // decision-kit blast-radius score
	Initiator Identity        // who/what started this (full attribution)
	Note      string          // optional transition note, persisted only to history
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Identity is the immutable attribution of who initiated an action —
// a human, a CI run, or a named agent. Captured for every transition.
type Identity struct {
	Kind   string   // "human" | "agent" | "ci"
	Name   string   // user id, agent name, or pipeline id
	Groups []string // external identity groups, when supplied by an IdP
}

// TargetState is the last observed fingerprint for a target, used for drift.
type TargetState struct {
	TargetRef  string
	Observed   target.Fingerprint
	ObservedAt time.Time
}

// ScheduledRollout queues a rollout for a specific future time.
type ScheduledRollout struct {
	ID        string
	TargetRef string
	DueAt     time.Time
	Desired   target.Manifest
	Initiator Identity
}

// RolloutRecord is an audit/history entry — believed-vs-actual over time.
type RolloutRecord struct {
	RolloutID string
	TargetRef string
	Phase     Phase
	Note      string
	Initiator Identity
	At        time.Time
}

// Dependency is a directed edge in the deployment DAG: From must complete
// before To. The same graph feeds the blast-radius risk signal.
type Dependency struct {
	From string // target ref that must complete first
	To   string // target ref that depends on From
}
