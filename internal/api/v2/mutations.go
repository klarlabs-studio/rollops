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
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
)

// Deployer is the write side this service projects.
//
// It is an interface so that the package depends on the calls it makes rather
// than on how a deploy.Service is assembled: that constructor takes a planner,
// a policy engine, a clock and six repositories, none of which the API layer
// has any business naming.
type Deployer interface {
	Plan(ctx context.Context, cmd deploy.PlanCommand) (plan.DeploymentPlan, error)
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
