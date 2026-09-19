// Package deploy turns a release and an environment into a plan somebody can
// review, and a reviewed plan into a deployment the engine may run.
//
// Nothing here changes infrastructure. Apply admits a deployment to the queue
// and stops; what carries it through applying and verifying is the engine,
// driven by events. The separation is the point: a plan may be approved hours
// after it was built, and it has to still mean the same thing when it is.
//
// The package depends on ports only. Which substrate an operation lands on,
// which policy language decided it, and which transport asked — none of that is
// visible from here (INV-006, INV-007).
package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/release"
)

var (
	// ErrCrossProject reports a release and an environment that do not belong
	// to the same project. Projects are the isolation boundary, so this is a
	// refusal rather than something to reconcile.
	ErrCrossProject = errors.New("deploy: release and environment are in different projects")

	// ErrNoTarget reports an environment with nothing to deploy to. An
	// environment may legitimately exist before its infrastructure does; it
	// just cannot receive a release yet.
	ErrNoTarget = errors.New("deploy: environment has no target to deploy to")

	// ErrPolicyRefused reports a plan policy did not allow. It is checked again
	// at apply rather than trusted from plan time, because the plan is the
	// record of the decision and the record is what apply reads.
	ErrPolicyRefused = errors.New("deploy: policy refused the plan")

	// ErrEnvironmentBusy reports an environment that already has a deployment
	// in flight. One environment runs one deployment at a time: two concurrent
	// applies would race over the same infrastructure and neither timeline
	// would describe what happened.
	ErrEnvironmentBusy = errors.New("deploy: environment already has a deployment in flight")

	// ErrIncomplete reports a service constructed without something it needs.
	// A missing dependency surfaces here rather than as a nil dereference on
	// the first deployment of the day.
	ErrIncomplete = errors.New("deploy: service is missing a dependency")
)

// PlanRequest is what a planner is asked to work out. Current is what last
// landed in the environment, or nil if nothing has: a planner needs it to
// describe a change rather than a creation, and to build the rollback.
type PlanRequest struct {
	Environment environment.Environment
	Release     release.Release
	Strategy    deployment.Strategy
	Current     *deployment.Deployment
}

// Proposal is what a planner worked out: the operations that would make the
// release effective, and how to undo them.
type Proposal struct {
	Operations []plan.PlannedOperation
	Rollback   plan.RollbackPlan
}

// Planner works out what would have to change. It is an interface because the
// answer depends entirely on the target — a Kubernetes planner diffs manifests,
// an SSH one compares file trees — and this layer must not know which.
type Planner interface {
	PlanDeployment(ctx context.Context, req PlanRequest) (Proposal, error)
}

// PolicyRequest is what policy is asked to rule on. The operations are included
// because policy is written against what would change, not only against where.
type PolicyRequest struct {
	Environment environment.Environment
	Release     release.Release
	Strategy    deployment.Strategy
	Operations  []plan.PlannedOperation
	Actor       identity.Principal
}

// PolicyEngine decides whether a proposal may be applied and what has to happen
// first. It is an interface for the same reason Planner is: the evaluation
// language is an adapter's business.
type PolicyEngine interface {
	Evaluate(ctx context.Context, req PolicyRequest) (policy.Decision, error)
}

// Config is what a Service needs. It is a struct rather than a long parameter
// list because every field is required and a positional constructor with nine
// arguments is a swap waiting to happen.
type Config struct {
	Transactor   port.Transactor
	Plans        port.PlanRepository
	Deployments  port.DeploymentRepository
	Releases     port.ReleaseRepository
	Environments port.EnvironmentRepository
	Planner      Planner
	Policy       PolicyEngine
	Clock        identity.Clock
	IDs          identity.Generator

	// PlanLifetime is how long a plan stays applicable. It bounds the window in
	// which the world can drift away from what was reviewed.
	PlanLifetime time.Duration
}

// Service plans deployments and admits them to the queue.
type Service struct {
	cfg Config
}

// New returns a service, or reports what it was not given.
func New(cfg Config) (*Service, error) {
	var missing []string
	for _, d := range []struct {
		name    string
		present bool
	}{
		{"transactor", cfg.Transactor != nil},
		{"plan repository", cfg.Plans != nil},
		{"deployment repository", cfg.Deployments != nil},
		{"release repository", cfg.Releases != nil},
		{"environment repository", cfg.Environments != nil},
		{"planner", cfg.Planner != nil},
		{"policy engine", cfg.Policy != nil},
		{"clock", cfg.Clock != nil},
		{"id generator", cfg.IDs != nil},
		{"plan lifetime", cfg.PlanLifetime > 0},
	} {
		if !d.present {
			missing = append(missing, d.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrIncomplete, strings.Join(missing, ", "))
	}
	return &Service{cfg: cfg}, nil
}

// PlanCommand asks what would happen if a release were deployed.
type PlanCommand struct {
	EnvironmentID identity.EnvironmentID
	ReleaseID     identity.ReleaseID
	Strategy      deployment.Strategy
	Actor         identity.Principal
}

// Plan works out what deploying the release would change, has policy rule on
// it, and stores the result.
//
// The plan is pinned to the environment revision it was computed against, so
// that an apply against a world that has since moved is refused rather than
// applied hopefully (spec 4.9).
func (s *Service) Plan(ctx context.Context, cmd PlanCommand) (plan.DeploymentPlan, error) {
	env, err := s.cfg.Environments.Get(ctx, cmd.EnvironmentID)
	if err != nil {
		return plan.DeploymentPlan{}, fmt.Errorf("deploy: environment %s: %w", cmd.EnvironmentID, err)
	}
	rel, err := s.cfg.Releases.Get(ctx, cmd.ReleaseID)
	if err != nil {
		return plan.DeploymentPlan{}, fmt.Errorf("deploy: release %s: %w", cmd.ReleaseID, err)
	}
	if rel.ProjectID != env.ProjectID {
		return plan.DeploymentPlan{}, fmt.Errorf("%w: release is in %s, environment in %s",
			ErrCrossProject, rel.ProjectID, env.ProjectID)
	}
	if !env.CanDeploy() {
		return plan.DeploymentPlan{}, fmt.Errorf("%w: %s", ErrNoTarget, env.Name)
	}

	current, err := s.current(ctx, env.ID)
	if err != nil {
		return plan.DeploymentPlan{}, err
	}

	proposal, err := s.cfg.Planner.PlanDeployment(ctx, PlanRequest{
		Environment: env,
		Release:     rel,
		Strategy:    cmd.Strategy,
		Current:     current,
	})
	if err != nil {
		return plan.DeploymentPlan{}, fmt.Errorf("deploy: planning: %w", err)
	}

	decision, err := s.cfg.Policy.Evaluate(ctx, PolicyRequest{
		Environment: env,
		Release:     rel,
		Strategy:    cmd.Strategy,
		Operations:  proposal.Operations,
		Actor:       cmd.Actor,
	})
	if err != nil {
		return plan.DeploymentPlan{}, fmt.Errorf("deploy: policy: %w", err)
	}

	p, err := plan.New(s.cfg.IDs, s.cfg.Clock, cmd.Actor, s.cfg.PlanLifetime, plan.DeploymentPlan{
		ProjectID:     env.ProjectID,
		EnvironmentID: env.ID,
		ReleaseID:     rel.ID,
		BaseRevision:  env.Revision,
		Strategy:      cmd.Strategy,
		Operations:    proposal.Operations,
		Policy:        decision,
		Rollback:      proposal.Rollback,
	})
	if err != nil {
		return plan.DeploymentPlan{}, err
	}
	if err := s.cfg.Plans.Create(ctx, p); err != nil {
		return plan.DeploymentPlan{}, fmt.Errorf("deploy: storing plan: %w", err)
	}
	return p, nil
}

// ApplyCommand asks for a stored plan to be carried out.
type ApplyCommand struct {
	PlanID  identity.PlanID
	Trigger deployment.Trigger
	Actor   identity.Principal
}

// Apply admits a deployment for the plan to the queue.
//
// It does not itself change anything — despite the name, which is the
// vocabulary the spec and every operator use. What it does is decide that the
// plan is still the plan, that policy still allows it, and that the environment
// is free; then it records a deployment for the engine to pick up. The strategy
// and the operations come from the stored plan rather than from the caller, so
// what runs is what was reviewed.
//
// A deployment whose plan carries unmet requirements is admitted in
// awaiting_approval rather than refused: the requirement is a gate to pass, not
// an error, and recording it is how an approver finds out there is one.
func (s *Service) Apply(ctx context.Context, cmd ApplyCommand) (deployment.Deployment, error) {
	p, err := s.cfg.Plans.Get(ctx, cmd.PlanID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf("deploy: plan %s: %w", cmd.PlanID, err)
	}
	env, err := s.cfg.Environments.Get(ctx, p.EnvironmentID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf("deploy: environment %s: %w", p.EnvironmentID, err)
	}
	// Tamper, expiry and staleness are reported unwrapped so that a caller can
	// tell them apart: each has a different recourse, and only one of them is
	// an incident.
	if err := p.CheckApplicable(s.cfg.Clock.Now(), env.Revision); err != nil {
		return deployment.Deployment{}, err
	}
	if !p.Policy.Allowed {
		return deployment.Deployment{}, fmt.Errorf("%w: %s", ErrPolicyRefused, reasons(p.Policy))
	}

	var admitted deployment.Deployment
	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		// The check and the write are in one transaction on purpose: checked
		// outside it, two applies arriving together would both find the
		// environment idle and both admit a deployment.
		active, err := s.cfg.Deployments.FindActive(ctx, env.ID)
		switch {
		case err == nil:
			return fmt.Errorf("%w: %s is %s", ErrEnvironmentBusy, active.ID, active.Status)
		case !errors.Is(err, port.ErrNotFound):
			return fmt.Errorf("deploy: checking for an active deployment: %w", err)
		}

		previous, err := s.current(ctx, env.ID)
		if err != nil {
			return err
		}

		d, err := deployment.New(s.cfg.IDs, s.cfg.Clock, cmd.Actor, deployment.Deployment{
			ProjectID:     p.ProjectID,
			EnvironmentID: p.EnvironmentID,
			ReleaseID:     p.ReleaseID,
			PlanID:        p.ID,
			Strategy:      p.Strategy,
			Trigger:       cmd.Trigger,
		})
		if err != nil {
			return err
		}
		if previous != nil {
			id := previous.ID
			d.Previous = &id
		}

		next := deployment.StatusQueued
		if !p.Policy.Satisfied() {
			next = deployment.StatusAwaitingApproval
		}
		if d, err = d.TransitionTo(next, s.cfg.Clock.Now()); err != nil {
			return err
		}
		rev, err := s.cfg.Deployments.Create(ctx, d)
		if err != nil {
			return fmt.Errorf("deploy: storing deployment: %w", err)
		}
		d.Revision = rev
		admitted = d
		return nil
	})
	if err != nil {
		return deployment.Deployment{}, err
	}
	return admitted, nil
}

// current returns the deployment that last succeeded in the environment. It is
// what a new deployment replaces and what a rollback would return to, and it is
// nil when nothing has ever landed. A failed or cancelled deployment is not it:
// what an environment is running is the last thing that worked.
func (s *Service) current(ctx context.Context, e identity.EnvironmentID) (*deployment.Deployment, error) {
	history, err := s.cfg.Deployments.ListForEnvironment(ctx, e)
	if err != nil {
		return nil, fmt.Errorf("deploy: environment history: %w", err)
	}
	for _, d := range history {
		if d.Status == deployment.StatusSucceeded {
			return &d, nil
		}
	}
	return nil, nil
}

// reasons renders why policy refused, so that the refusal an operator sees says
// what to do about it rather than only that it happened.
func reasons(d policy.Decision) string {
	if len(d.Reasons) == 0 {
		return "no reason given"
	}
	out := make([]string, len(d.Reasons))
	for i, r := range d.Reasons {
		out[i] = fmt.Sprintf("%s: %s", r.Code, r.Message)
	}
	return strings.Join(out, "; ")
}
