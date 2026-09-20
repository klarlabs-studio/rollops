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
	// ErrNotPromotable reports a deployment that cannot be promoted from where
	// it is. Nothing has been observed serving yet, so there is nothing to
	// widen.
	ErrNotPromotable = errors.New("deploy: deployment cannot be promoted")

	// ErrUnexplainedPromotion reports an override that does not say why. A
	// paused deployment was stopped by something; promoting it anyway is a
	// decision to disregard that, and whoever finds the release out in front of
	// everyone has to be able to find out who decided so.
	ErrUnexplainedPromotion = errors.New("deploy: promotion past a pause does not say why")
)

// deploymentPromoting says the release was widened. From is the status it was
// promoted out of, because `promoting` alone does not tell an ordinary
// promotion from an override: reaching it from `verifying` means the checks
// were happy, and reaching it from `paused` means somebody decided they did not
// matter. Reason is empty for the first and required for the second.
type deploymentPromoting struct {
	PlanID identity.PlanID   `json:"plan_id"`
	From   deployment.Status `json:"from"`
	Reason string            `json:"reason,omitempty"`
}

// PromoteCommand widens a deployment from wherever it is serving to the rest of
// its fleet.
type PromoteCommand struct {
	DeploymentID identity.DeploymentID
	Reason       string
	Actor        identity.Principal
}

// Promote records the decision to widen a deployment and stops.
//
// Nothing is dispatched. Shifting traffic is the engine's work against the
// plan's operations, and doing it here would make this the one place in the
// package that mutates a substrate — the same line Apply holds.
//
// What may be promoted is whatever has an edge to `promoting` (§4.8), so a
// status gaining or losing that edge changes this without changing this code.
// In particular nothing here re-reads the verdict: a deployment that failed
// verification is `paused` rather than `verifying`, so the state machine
// already knows which of the two roads this is, and asking the log again would
// be a second copy of a fact that could disagree with the first.
func (s *Service) Promote(
	ctx context.Context, cmd PromoteCommand,
) (deployment.Deployment, error) {
	d, err := s.cfg.Deployments.Get(ctx, cmd.DeploymentID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf(
			"deploy: deployment %s: %w", cmd.DeploymentID, err)
	}
	if !d.Status.CanTransitionTo(deployment.StatusPromoting) {
		return deployment.Deployment{}, fmt.Errorf(
			"%w: %s is %s", ErrNotPromotable, d.ID, d.Status)
	}
	// Only the override has to justify itself. A verification that passed is
	// the ordinary road to promotion, and requiring prose for the expected next
	// step teaches operators to type anything to get past the prompt.
	if d.Status == deployment.StatusPaused && strings.TrimSpace(cmd.Reason) == "" {
		return deployment.Deployment{}, fmt.Errorf("%w: %s", ErrUnexplainedPromotion, d.ID)
	}

	var out deployment.Deployment
	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		moved, err := d.TransitionTo(deployment.StatusPromoting, s.cfg.Clock.Now())
		if err != nil {
			return err
		}
		rev, err := s.cfg.Deployments.Update(ctx, moved)
		if err != nil {
			return fmt.Errorf("deploy: storing the deployment: %w", err)
		}
		moved.Revision = rev
		if err := s.recordPromotion(ctx, cmd, d.Status, moved); err != nil {
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

func (s *Service) recordPromotion(
	ctx context.Context,
	cmd PromoteCommand,
	from deployment.Status,
	d deployment.Deployment,
) error {
	payload, err := encode(deploymentPromoting{
		PlanID: d.PlanID,
		From:   from,
		Reason: strings.TrimSpace(cmd.Reason),
	})
	if err != nil {
		return err
	}
	return s.recordAgainst(ctx, cmd.Actor, d, event.DeploymentPromotionStarted, payload)
}
