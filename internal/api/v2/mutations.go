// The write half of the v2 service.
//
// Nothing here decides anything. The rules about what may be planned, whether
// a plan is still applicable and who has to approve it live in
// internal/app/deploy, and this file's job is to parse identifiers, carry the
// idempotency key and turn the answer into a view. A rule that leaked up here
// would be a rule the engine does not have.
//
// The actor is taken as identity.Principal rather than as the Principal view.
// It is an input established by the transport's authentication, not something
// parsed from a request body, and policy is evaluated against its claims — a
// view that dropped them would decide differently from the CLI.
package apiv2

import (
	"context"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

// Deployer is the write side this service projects.
//
// It is an interface so that the package depends on the calls it makes rather
// than on how a deploy.Service is assembled: that constructor takes a planner,
// a policy engine, a clock and six repositories, none of which the API layer
// has any business naming.
type Deployer interface {
	Plan(ctx context.Context, cmd deploy.PlanCommand) (plan.DeploymentPlan, error)
	Apply(ctx context.Context, cmd deploy.ApplyCommand) (deployment.Deployment, error)
	Approve(ctx context.Context, cmd deploy.ApproveCommand) (deployment.Deployment, error)
	Cancel(ctx context.Context, cmd deploy.CancelCommand) (deployment.Deployment, error)
	Verify(ctx context.Context, cmd deploy.VerifyCommand) (deployment.Deployment, deploy.Verification, error)
	Promote(ctx context.Context, cmd deploy.PromoteCommand) (deployment.Deployment, error)
	Rollback(ctx context.Context, cmd deploy.RollbackCommand) (deployment.Deployment, error)
}

// CreatePlanRequest asks what deploying a release to an environment would
// change.
//
// Planning has no effect on infrastructure (§8.2), but it does store a plan
// and charge a target for the work of computing one, so it carries a key like
// any other mutation.
type CreatePlanRequest struct {
	EnvironmentID string
	ReleaseID     string

	// Strategy is how the release should be rolled out. Empty is refused
	// rather than defaulted: the strategy is what the plan's operations are
	// built for, and guessing it would have an approver review a rollout
	// nobody asked for.
	Strategy string

	Actor identity.Principal

	// IdempotencyKey lets a retry return the plan the first call created
	// instead of building a second one against a world that has since moved.
	IdempotencyKey string
}

// CreatePlan works out what deploying the release would change, has policy
// rule on it, and returns the stored plan.
func (s *Service) CreatePlan(ctx context.Context, req CreatePlanRequest) (Plan, error) {
	environmentID, err := identity.ParseEnvironmentID(req.EnvironmentID)
	if err != nil {
		return Plan{}, badArgument("apiv2: environment id: %w", err)
	}
	releaseID, err := identity.ParseReleaseID(req.ReleaseID)
	if err != nil {
		return Plan{}, badArgument("apiv2: release id: %w", err)
	}
	strategy := deployment.Strategy(req.Strategy)
	if !strategy.Valid() {
		return Plan{}, badArgument("apiv2: unknown strategy %q", req.Strategy)
	}

	fp := fingerprint(string(environmentID), string(releaseID), req.Strategy, req.Actor.ID)
	return once(ctx, s, opCreatePlan, req.IdempotencyKey, fp,
		func(ctx context.Context) (string, Plan, error) {
			p, err := s.deployer.Plan(ctx, deploy.PlanCommand{
				EnvironmentID: environmentID,
				ReleaseID:     releaseID,
				Strategy:      strategy,
				Actor:         req.Actor,
			})
			if err != nil {
				return "", Plan{}, failure("apiv2: plan %s for %s: %w", releaseID, environmentID, err)
			}
			// Redacted for the same reason GetPlan redacts: the plan is being
			// rendered, and a change marked sensitive carries a value that
			// must not leave the process (INV-012).
			return string(p.ID), viewPlan(p.Redacted()), nil
		},
		func(ctx context.Context, id string) (Plan, error) {
			return s.GetPlan(ctx, GetPlanRequest{ID: id})
		},
	)
}

// ApplyPlanRequest asks for a stored plan to be carried out.
//
// The strategy and the operations are not here. They come from the plan, so
// what runs is what was reviewed — a request that could restate them would let
// an applier diverge from the thing an approver read.
type ApplyPlanRequest struct {
	PlanID string

	// Detail is free text for whoever reads the timeline later: a ticket, a
	// change request, the reason somebody pressed the button. The trigger type
	// is not the caller's to choose — this endpoint is the API, and a request
	// that could claim "git" or "schedule" would write a cause into the record
	// that nothing else corroborates.
	Detail string

	Actor          identity.Principal
	IdempotencyKey string
}

// ApplyPlan admits a deployment for the plan.
//
// It returns as soon as the deployment is recorded, which is what §23.3 asks
// of a mutation whose execution is asynchronous: the identity comes back now
// and the caller follows the timeline for the rest. A deployment whose plan
// carries unmet requirements comes back awaiting_approval rather than as an
// error — the gate is something to pass, and returning it is how an approver
// finds out there is one.
func (s *Service) ApplyPlan(ctx context.Context, req ApplyPlanRequest) (Deployment, error) {
	planID, err := identity.ParsePlanID(req.PlanID)
	if err != nil {
		return Deployment{}, badArgument("apiv2: plan id: %w", err)
	}

	fp := fingerprint(string(planID), req.Detail, req.Actor.ID)
	return once(ctx, s, opApplyPlan, req.IdempotencyKey, fp,
		func(ctx context.Context) (string, Deployment, error) {
			d, err := s.deployer.Apply(ctx, deploy.ApplyCommand{
				PlanID:  planID,
				Trigger: deployment.Trigger{Type: deployment.TriggerAPI, Detail: req.Detail},
				Actor:   req.Actor,
			})
			if err != nil {
				return "", Deployment{}, failure("apiv2: apply plan %s: %w", planID, err)
			}
			return string(d.ID), viewDeployment(d), nil
		},
		func(ctx context.Context, id string) (Deployment, error) {
			return s.GetDeployment(ctx, GetDeploymentRequest{ID: id})
		},
	)
}

// CancelDeploymentRequest stops a deployment before it reaches an outcome of
// its own.
type CancelDeploymentRequest struct {
	DeploymentID string

	// Reason is why, and it is required. A deployment in the cancelled status
	// with nothing saying who stopped it is indistinguishable from one that
	// died, and whoever finds it stopped has nothing to act on.
	Reason string

	Actor          identity.Principal
	IdempotencyKey string
}

// CancelDeployment stops the deployment and returns it.
//
// Nothing is undone. Cancelling a deployment that had begun applying leaves
// what it had already changed in place — putting that back is a rollback,
// which is a deployment of its own and something a caller asks for separately.
func (s *Service) CancelDeployment(ctx context.Context, req CancelDeploymentRequest) (Deployment, error) {
	deploymentID, err := identity.ParseDeploymentID(req.DeploymentID)
	if err != nil {
		return Deployment{}, badArgument("apiv2: deployment id: %w", err)
	}

	fp := fingerprint(string(deploymentID), req.Reason, req.Actor.ID)
	return once(ctx, s, opCancelDeployment, req.IdempotencyKey, fp,
		func(ctx context.Context) (string, Deployment, error) {
			d, err := s.deployer.Cancel(ctx, deploy.CancelCommand{
				DeploymentID: deploymentID,
				Reason:       req.Reason,
				Actor:        req.Actor,
			})
			if err != nil {
				return "", Deployment{}, failure("apiv2: cancel deployment %s: %w", deploymentID, err)
			}
			return string(d.ID), viewDeployment(d), nil
		},
		func(ctx context.Context, id string) (Deployment, error) {
			return s.GetDeployment(ctx, GetDeploymentRequest{ID: id})
		},
	)
}

// ApproveDeploymentRequest records one principal's answer about a gated
// deployment.
type ApproveDeploymentRequest struct {
	DeploymentID string

	// PlanHash is the hash of the plan the approver read, and it is required.
	// §13 binds an approval to a revision: without one, consent given for what
	// somebody reviewed would carry over to whatever happens to be stored when
	// it is spent.
	PlanHash string

	// Granted says whether the answer is yes. It is a bool here and a
	// tri-state below because a request either states an answer or is
	// malformed, where a stored approval that was never filled in must not
	// read as a yes.
	Granted bool

	// Reason is why. It is what an auditor reads, and a denial without one
	// tells whoever has to act on it nothing.
	Reason string

	Actor          identity.Principal
	IdempotencyKey string
}

// ApproveDeployment records the answer and returns the deployment, moved if
// the answer settled the matter.
//
// The deployment comes back rather than a bare acknowledgement because one
// approval against a two-approval requirement records the answer and leaves
// the gate shut: the caller has to be able to see whether it moved.
func (s *Service) ApproveDeployment(ctx context.Context, req ApproveDeploymentRequest) (Deployment, error) {
	deploymentID, err := identity.ParseDeploymentID(req.DeploymentID)
	if err != nil {
		return Deployment{}, badArgument("apiv2: deployment id: %w", err)
	}
	hash, err := digest.Parse(req.PlanHash)
	if err != nil {
		return Deployment{}, badArgument("apiv2: plan hash: %w", err)
	}
	decision := policy.ApprovalDenied
	if req.Granted {
		decision = policy.ApprovalGranted
	}

	fp := fingerprint(string(deploymentID), hash.String(), string(decision), req.Reason, req.Actor.ID)
	return once(ctx, s, opApproveDeployment, req.IdempotencyKey, fp,
		func(ctx context.Context) (string, Deployment, error) {
			d, err := s.deployer.Approve(ctx, deploy.ApproveCommand{
				DeploymentID: deploymentID,
				Revision:     hash,
				Decision:     decision,
				Reason:       req.Reason,
				Actor:        req.Actor,
			})
			if err != nil {
				return "", Deployment{}, failure("apiv2: approve deployment %s: %w", deploymentID, err)
			}
			return string(d.ID), viewDeployment(d), nil
		},
		func(ctx context.Context, id string) (Deployment, error) {
			return s.GetDeployment(ctx, GetDeploymentRequest{ID: id})
		},
	)
}
