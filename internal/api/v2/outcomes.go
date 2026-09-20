// How a deployment that is already running ends.
//
// Promoting and rolling back both hand the deployment to the engine and stop,
// for the reason applying does: this layer records what was asked for and the
// engine is what touches a substrate. Neither returns a verdict, an operation
// list or a progress figure — what comes back is the deployment, whose status
// is the only thing that has changed yet.
package apiv2

import (
	"context"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// PromoteDeploymentRequest widens a deployment that is serving.
type PromoteDeploymentRequest struct {
	DeploymentID string

	// Reason is why, and it is required only for a deployment that is paused.
	// Promoting one of those disregards whatever paused it, so somebody has to
	// be findable for the decision; promoting one whose checks are passing
	// heeds the signal and has nothing to explain.
	Reason string

	Actor          identity.Principal
	IdempotencyKey string
}

// PromoteDeployment hands the widening to the engine and returns the
// deployment.
func (s *Service) PromoteDeployment(
	ctx context.Context, req PromoteDeploymentRequest,
) (Deployment, error) {
	deploymentID, err := identity.ParseDeploymentID(req.DeploymentID)
	if err != nil {
		return Deployment{}, badArgument("apiv2: deployment id: %w", err)
	}

	fp := fingerprint(string(deploymentID), req.Reason, req.Actor.ID)
	return once(ctx, s, opPromoteDeployment, req.IdempotencyKey, fp,
		func(ctx context.Context) (string, Deployment, error) {
			d, err := s.deployer.Promote(ctx, deploy.PromoteCommand{
				DeploymentID: deploymentID,
				Reason:       req.Reason,
				Actor:        req.Actor,
			})
			if err != nil {
				return "", Deployment{}, failure("apiv2: promote deployment %s: %w", deploymentID, err)
			}
			return string(d.ID), viewDeployment(d), nil
		},
		func(ctx context.Context, id string) (Deployment, error) {
			return s.GetDeployment(ctx, GetDeploymentRequest{ID: id})
		},
	)
}

// RollbackDeploymentRequest returns a deployment to the release it replaced.
type RollbackDeploymentRequest struct {
	DeploymentID string

	// Reason is why, and unlike promoting past a pause it is optional. A
	// rollback heeds a signal rather than disregarding one, and §11.4's
	// configured `onFailure: rollback` fires with nobody present to write
	// prose — requiring it would make that branch impossible to take.
	Reason string

	Actor          identity.Principal
	IdempotencyKey string
}

// RollbackDeployment hands the reversal to the engine and returns the
// deployment.
//
// Nothing is undone here. The plan's rollback operations are the engine's to
// run, and a plan that declares none is refused rather than started: §23.4's
// conflict, because the request is well formed and what is wrong is the plan
// it names.
func (s *Service) RollbackDeployment(
	ctx context.Context, req RollbackDeploymentRequest,
) (Deployment, error) {
	deploymentID, err := identity.ParseDeploymentID(req.DeploymentID)
	if err != nil {
		return Deployment{}, badArgument("apiv2: deployment id: %w", err)
	}

	fp := fingerprint(string(deploymentID), req.Reason, req.Actor.ID)
	return once(ctx, s, opRollbackDeployment, req.IdempotencyKey, fp,
		func(ctx context.Context) (string, Deployment, error) {
			d, err := s.deployer.Rollback(ctx, deploy.RollbackCommand{
				DeploymentID: deploymentID,
				Reason:       req.Reason,
				Actor:        req.Actor,
			})
			if err != nil {
				return "", Deployment{}, failure("apiv2: roll back deployment %s: %w", deploymentID, err)
			}
			return string(d.ID), viewDeployment(d), nil
		},
		func(ctx context.Context, id string) (Deployment, error) {
			return s.GetDeployment(ctx, GetDeploymentRequest{ID: id})
		},
	)
}
