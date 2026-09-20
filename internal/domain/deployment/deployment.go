// Package deployment records the attempt to make one release effective in one
// environment.
//
// A deployment is the record, not the mechanism. It says which release, which
// environment, under which plan, on whose authority and how far it got; how the
// change is actually made is a target's concern and is never stored here
// (spec 4.7). Target-specific state belongs to child records and events, so
// that this aggregate stays the same shape whatever it is deploying to.
package deployment

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
)

// ErrIllegalTransition reports a status change the machine does not allow.
var ErrIllegalTransition = errors.New("deployment: illegal status transition")

// Strategy names how a release is rolled out. Its parameters — step weights,
// bake times, traffic splits — belong to the environment's target binding, not
// here: two deployments run under one strategy differ in what they deploy, not
// in how the strategy is configured.
type Strategy string

const (
	StrategyRolling   Strategy = "rolling"
	StrategyCanary    Strategy = "canary"
	StrategyBlueGreen Strategy = "blue_green"
	StrategyRecreate  Strategy = "recreate"
)

// strategies is the whole set, in the order a rollout gets riskier to operate:
// recreate takes the service down, rolling replaces in place, and the last two
// run old and new side by side. The order is published, so it is chosen rather
// than alphabetical.
var strategies = []Strategy{
	StrategyRecreate, StrategyRolling, StrategyCanary, StrategyBlueGreen,
}

// Strategies returns every rollout strategy the system knows, in a stable
// order. A transport that has to name the whole vocabulary asks here rather
// than restating it, and the slice is rebuilt on each call so a caller cannot
// edit the set through it.
func Strategies() []Strategy { return slices.Clone(strategies) }

// Valid reports whether s is a rollout strategy the system knows. It is
// exported because a plan records the strategy it was reviewed under and has
// to check it without restating the list — and it reads the same slice
// Strategies publishes, so the two cannot disagree about what exists.
func (s Strategy) Valid() bool { return slices.Contains(strategies, s) }

// TriggerType names what set a deployment going. It is separate from the actor
// because "who" and "why" are different questions: a scheduled deployment still
// has a principal, and a human-run one still has a cause.
type TriggerType string

const (
	TriggerManual    TriggerType = "manual"
	TriggerAPI       TriggerType = "api"
	TriggerGit       TriggerType = "git"
	TriggerSchedule  TriggerType = "schedule"
	TriggerPromotion TriggerType = "promotion"
	TriggerRollback  TriggerType = "rollback"
)

func (t TriggerType) valid() bool {
	switch t {
	case TriggerManual, TriggerAPI, TriggerGit,
		TriggerSchedule, TriggerPromotion, TriggerRollback:
		return true
	}
	return false
}

// Trigger is what caused the deployment. Detail is free text for the person
// reading the timeline — a commit subject, a schedule name, a ticket.
type Trigger struct {
	Type   TriggerType
	Detail string
}

// Deployment records one attempt to make a release effective in an environment.
//
// Revision is the revision the deployment was read at; a repository tests it
// before writing and refuses a change built on a stale read (ADR-0003). Unlike
// a release, a deployment changes, so it has a race to lose.
type Deployment struct {
	ID            identity.DeploymentID
	ProjectID     identity.ProjectID
	EnvironmentID identity.EnvironmentID
	ReleaseID     identity.ReleaseID
	PlanID        identity.PlanID

	Strategy Strategy
	Status   Status
	Trigger  Trigger
	Actor    identity.Principal

	CreatedAt time.Time

	// StartedAt is when the deployment began changing the world, which is when
	// it entered applying and not when it was created. FinishedAt is when it
	// reached an outcome. Both are absent until then rather than zero-valued,
	// so "not started" is distinguishable from "started at the epoch".
	StartedAt  *time.Time
	FinishedAt *time.Time

	// Previous is the deployment this one replaces, which is what makes a
	// rollback target derivable from the record rather than inferred.
	Previous *identity.DeploymentID

	Revision identity.Revision
}

// New stamps identity, attribution and time onto d and puts it in the planned
// status.
//
// The actor's claims are redacted first: a deployment is persisted and rendered
// on every surface, so a credential that reached it would be impossible to
// recall (INV-012).
func New(g identity.Generator, c identity.Clock, by identity.Principal, d Deployment) (Deployment, error) {
	id, err := identity.NewDeploymentID(g)
	if err != nil {
		return Deployment{}, err
	}
	d.ID = id
	d.Actor = by.Redacted()
	d.CreatedAt = c.Now()
	d.Status = StatusPlanned
	d.StartedAt = nil
	d.FinishedAt = nil
	if err := d.Validate(); err != nil {
		return Deployment{}, err
	}
	return d, nil
}

// Validate reports whether the deployment is a complete, auditable record.
func (d Deployment) Validate() error {
	for _, f := range []struct {
		name, value string
	}{
		{"id", string(d.ID)},
		{"project id", string(d.ProjectID)},
		{"environment id", string(d.EnvironmentID)},
		{"release id", string(d.ReleaseID)},
		// A deployment without a plan was never reviewed. Apply has nothing to
		// verify the hash of and nothing to compare the world against.
		{"plan id", string(d.PlanID)},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("deployment: no %s", f.name)
		}
	}
	if !d.Strategy.Valid() {
		return fmt.Errorf("deployment: unknown strategy %q", d.Strategy)
	}
	if !d.Trigger.Type.valid() {
		return fmt.Errorf("deployment: unknown trigger type %q", d.Trigger.Type)
	}
	if !d.Status.Valid() {
		return fmt.Errorf("deployment: unknown status %q", d.Status)
	}
	if err := d.Actor.Validate(); err != nil {
		return fmt.Errorf("deployment: %w", err)
	}
	if d.CreatedAt.IsZero() {
		return errors.New("deployment: no creation time")
	}
	return nil
}

// TransitionTo returns a copy of the deployment in status next, stamping the
// start and finish times the move implies. The receiver is untouched, so a
// transition whose write fails can simply be discarded.
func (d Deployment) TransitionTo(next Status, at time.Time) (Deployment, error) {
	if !d.Status.CanTransitionTo(next) {
		return Deployment{}, fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, d.Status, next)
	}
	d.Status = next
	// Only the first entry into applying starts the clock. A canary that is
	// paused and resumed has not started again, and re-stamping would make a
	// deployment's duration depend on how often an operator held it.
	if next == StatusApplying && d.StartedAt == nil {
		d.StartedAt = &at
	}
	if next.IsTerminal() {
		d.FinishedAt = &at
	}
	return d, nil
}
