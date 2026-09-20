package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
)

var (
	// ErrNotReversible reports a deployment that cannot be rolled back from
	// where it is. One that has changed nothing has nothing to put back.
	ErrNotReversible = errors.New("deploy: deployment cannot be rolled back")

	// ErrNoWayBack reports a deployment whose plan declares no rollback. A plan
	// need not have one — not every change has a defined way back — and the
	// honest answer is to say so rather than to start a reversal that has no
	// operations to run.
	ErrNoWayBack = errors.New("deploy: the plan declares no way back")
)

// rollbackStarted says a deployment is being put back. From is the status it
// was reversed out of, and ToRelease is where it is going: an operator watching
// a rollback wants to know which release is coming back, and the plan that says
// so expires while the timeline does not.
type rollbackStarted struct {
	PlanID      identity.PlanID    `json:"plan_id"`
	From        deployment.Status  `json:"from"`
	FromRelease identity.ReleaseID `json:"from_release"`
	ToRelease   identity.ReleaseID `json:"to_release"`
	Operations  []string           `json:"operations,omitempty"`
	Reason      string             `json:"reason,omitempty"`
}

// RollbackCommand returns a deployment to the release it replaced.
type RollbackCommand struct {
	DeploymentID identity.DeploymentID
	Reason       string
	Actor        identity.Principal
}

// Rollback records the decision to reverse a deployment and stops.
//
// Nothing is undone here. The engine runs the plan's rollback operations, for
// the reason promoting does not shift traffic here: this package records
// intent and never touches a substrate.
//
// Unlike promoting past a pause this needs no justification. Rolling back heeds
// a signal rather than disregarding one, and §11.4's `onFailure: rollback`
// fires with no person present to write prose — requiring it would make a
// configured branch of the policy impossible to take.
func (s *Service) Rollback(
	ctx context.Context, cmd RollbackCommand,
) (deployment.Deployment, error) {
	d, err := s.cfg.Deployments.Get(ctx, cmd.DeploymentID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf(
			"deploy: deployment %s: %w", cmd.DeploymentID, err)
	}
	if !d.Status.CanTransitionTo(deployment.StatusRollingBack) {
		return deployment.Deployment{}, fmt.Errorf(
			"%w: %s is %s", ErrNotReversible, d.ID, d.Status)
	}
	p, err := s.cfg.Plans.Get(ctx, d.PlanID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf("deploy: plan %s: %w", d.PlanID, err)
	}
	// Moving to rolling_back with nothing to run would hand the engine an empty
	// instruction and leave the deployment in a status nothing can advance.
	if len(p.Rollback.Operations) == 0 {
		return deployment.Deployment{}, fmt.Errorf("%w: %s", ErrNoWayBack, d.ID)
	}

	var out deployment.Deployment
	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		moved, err := d.TransitionTo(deployment.StatusRollingBack, s.cfg.Clock.Now())
		if err != nil {
			return err
		}
		rev, err := s.cfg.Deployments.Update(ctx, moved)
		if err != nil {
			return fmt.Errorf("deploy: storing the deployment: %w", err)
		}
		moved.Revision = rev
		if err := s.recordRollback(ctx, cmd, d.Status, moved, p.Rollback); err != nil {
			return err
		}
		out = moved
		return nil
	})
	if err != nil {
		return deployment.Deployment{}, err
	}
	return out, nil
}

func (s *Service) recordRollback(
	ctx context.Context,
	cmd RollbackCommand,
	from deployment.Status,
	d deployment.Deployment,
	back plan.RollbackPlan,
) error {
	payload, err := encode(rollbackStarted{
		PlanID:      d.PlanID,
		From:        from,
		FromRelease: back.FromRelease,
		ToRelease:   back.ToRelease,
		Operations:  operationIDs(back.Operations),
		Reason:      strings.TrimSpace(cmd.Reason),
	})
	if err != nil {
		return err
	}
	return s.recordAgainst(ctx, cmd.Actor, d, event.RollbackStarted, payload)
}

// operationIDs names the operations the reversal will run. The identifiers are
// what the plan holds and what the engine's own events will report against, so
// a half-finished rollback can be read as one — the summaries are prose and the
// diffs carry the values that were redacted out of the plan (INV-012).
func operationIDs(ops []plan.PlannedOperation) []string {
	out := make([]string, len(ops))
	for i, o := range ops {
		out[i] = string(o.ID)
	}
	return out
}
