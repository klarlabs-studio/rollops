package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

var (
	// ErrNotCancellable reports a deployment that cannot be stopped from where
	// it is. Past the end there is nothing to stop, and reporting success would
	// tell an operator they had prevented something that had already happened.
	ErrNotCancellable = errors.New("deploy: deployment cannot be cancelled")

	// ErrUnexplainedCancellation reports a cancellation that does not say why.
	// Whoever finds the deployment stopped has to be able to find out what
	// stopped it, which is the same rule a denied approval is held to.
	ErrUnexplainedCancellation = errors.New("deploy: cancellation does not say why")
)

// deploymentCancelled says an operator intervened. From is the status the
// deployment was stopped out of: `cancelled` alone does not distinguish a
// release pulled from the queue from one abandoned halfway through applying.
type deploymentCancelled struct {
	PlanID identity.PlanID   `json:"plan_id"`
	From   deployment.Status `json:"from"`
	Reason string            `json:"reason"`
}

// CancelCommand stops a deployment before it reaches an outcome of its own.
type CancelCommand struct {
	DeploymentID identity.DeploymentID
	Reason       string
	Actor        identity.Principal
}

// Cancel stops a deployment and frees its environment.
//
// The refusal is the state machine's rather than a list of statuses kept here:
// what may be cancelled is whatever has an edge to `cancelled` (§4.8), so a
// status gaining or losing that edge changes this without changing this code.
//
// Nothing is undone. Cancelling a deployment that had begun applying leaves
// whatever it had already changed in place — putting that back is a rollback,
// which is a deployment of its own and not a side effect of stopping one.
func (s *Service) Cancel(ctx context.Context, cmd CancelCommand) (deployment.Deployment, error) {
	if strings.TrimSpace(cmd.Reason) == "" {
		return deployment.Deployment{}, ErrUnexplainedCancellation
	}
	d, err := s.cfg.Deployments.Get(ctx, cmd.DeploymentID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf("deploy: deployment %s: %w", cmd.DeploymentID, err)
	}
	if !d.Status.CanTransitionTo(deployment.StatusCancelled) {
		return deployment.Deployment{}, fmt.Errorf("%w: %s is %s", ErrNotCancellable, d.ID, d.Status)
	}

	var out deployment.Deployment
	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		stopped, err := d.TransitionTo(deployment.StatusCancelled, s.cfg.Clock.Now())
		if err != nil {
			return err
		}
		rev, err := s.cfg.Deployments.Update(ctx, stopped)
		if err != nil {
			return fmt.Errorf("deploy: storing the deployment: %w", err)
		}
		stopped.Revision = rev
		if err := s.recordCancellation(ctx, cmd, d.Status, stopped); err != nil {
			return err
		}
		out = stopped
		return nil
	})
	if err != nil {
		return deployment.Deployment{}, err
	}
	return out, nil
}

// recordCancellation puts the intervention on the deployment's timeline. This
// event means somebody stopped the deployment, not merely that its status is
// `cancelled`: a deployment cancelled by a denied approval already carries the
// rejection, and recording this beside it would put two causes on the timeline
// for one act.
func (s *Service) recordCancellation(
	ctx context.Context,
	cmd CancelCommand,
	from deployment.Status,
	d deployment.Deployment,
) error {
	payload, err := encode(deploymentCancelled{
		PlanID: d.PlanID,
		From:   from,
		Reason: cmd.Reason,
	})
	if err != nil {
		return err
	}
	return s.recordAgainst(ctx, cmd.Actor, d, event.DeploymentCancelled, payload)
}
