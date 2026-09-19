// Package planner works out what deploying a release to an environment would
// change, without changing it.
//
// It is the deploy.Planner the application layer calls, and it is a dispatcher
// rather than a planner of anything itself: an environment lists target
// bindings, each binding resolves to a target that knows its own substrate, and
// the proposal is what those targets said. Nothing here knows what a Kubernetes
// manifest is (INV-007).
//
// Planning is side-effect free (spec 8.2). The only target method reached from
// here is Plan, which the contract already requires to be free of side effects;
// Apply is not called, and a test asserts it.
//
// Two things a target reports are deliberately dropped. Its free-form diff and
// rendered bytes are whatever its CLI prints, which for most substrates
// includes the values of secrets — and a plan is persisted and rendered on
// every surface (INV-012). Spec 8.3 permits attaching a versioned opaque
// provider payload, and this declines the offer: the portable summary carries
// the description instead, and structured plan.Change values will carry
// field-level detail once targets report it in a form that can be marked
// sensitive.
package planner

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/release"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

var (
	// ErrNothingToDo reports an environment already at the release. It is not a
	// failure — it is the answer — but it is an error rather than an empty
	// proposal because a plan with no operations cannot be applied, and the
	// domain refuses to build one.
	ErrNothingToDo = errors.New("planner: every target is already at this release")

	// ErrBlocked reports a target that named a reason the apply would fail.
	// Planning stops rather than proposing the operations that would have
	// worked: the point of planning separately is to find this out before
	// anything moves, and a partial apply is what that exists to prevent.
	ErrBlocked = errors.New("planner: a target reported that the change cannot be applied")
)

// TargetRequest asks for a live target for one of an environment's bindings.
// The environment travels with the binding because resolving the binding's
// configuration needs the environment's variables, and because an error that
// cannot name where it came from is one an operator cannot act on.
type TargetRequest struct {
	Environment environment.Environment
	Binding     environment.TargetBinding
}

// Targets resolves a binding to the target it names.
type Targets interface {
	Resolve(ctx context.Context, req TargetRequest) (*targetv2.Bound, error)
}

// DesiredRequest asks what a target should converge on for a release.
type DesiredRequest struct {
	Environment environment.Environment
	Binding     environment.TargetBinding
	Release     release.Release
}

// Desired renders a release into the state one target should hold.
//
// It is a port rather than something this package works out, because turning a
// release's artifacts into a substrate's desired state is the substrate's
// business — a Helm chart, a compose file, a manifest directory — and deciding
// it here would put every packaging format in the layer meant to know none of
// them.
type Desired interface {
	DesiredState(ctx context.Context, req DesiredRequest) (targetv2.DesiredState, error)
}

// Planner proposes what a deployment would do.
type Planner struct {
	targets Targets
	desired Desired
}

// New returns a planner, or reports the port that was not supplied. Both are
// required: a planner missing either cannot plan anything, and finding that out
// at startup beats finding it out on the first deployment.
func New(targets Targets, desired Desired) (*Planner, error) {
	var missing []string
	if targets == nil {
		missing = append(missing, "targets")
	}
	if desired == nil {
		missing = append(missing, "desired state")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("planner: no %s", strings.Join(missing, " and no "))
	}
	return &Planner{targets: targets, desired: desired}, nil
}

// PlanDeployment works out what would change, binding by binding.
//
// Bindings are visited in the order the environment declares them, and the
// operations come out in that order. That order is part of the plan hash, so
// reordering it would give two plans that mean the same thing two identities —
// and an approval bound to one of them would not discharge the other (spec 13).
func (p *Planner) PlanDeployment(ctx context.Context, req deploy.PlanRequest) (deploy.Proposal, error) {
	var proposal deploy.Proposal
	for _, b := range req.Environment.Targets {
		op, wanted, err := p.planBinding(ctx, req, b)
		if err != nil {
			return deploy.Proposal{}, err
		}
		if !wanted {
			continue
		}
		proposal.Operations = append(proposal.Operations, op)
	}
	if len(proposal.Operations) == 0 {
		return deploy.Proposal{}, ErrNothingToDo
	}
	proposal.Rollback = rollback(req, proposal.Operations)
	return proposal, nil
}

// planBinding asks one target what it would do. The second result is false for
// a target that reported nothing to change, which is not an error and not an
// operation either: proposing a no-op would put a step in the plan that an
// operator has to read and that apply has to skip.
func (p *Planner) planBinding(
	ctx context.Context, req deploy.PlanRequest, b environment.TargetBinding,
) (plan.PlannedOperation, bool, error) {
	desired, err := p.desired.DesiredState(ctx, DesiredRequest{
		Environment: req.Environment,
		Binding:     b,
		Release:     req.Release,
	})
	if err != nil {
		return plan.PlannedOperation{}, false, fmt.Errorf(
			"planner: %s: rendering release %s: %w", b, req.Release.Version, err)
	}

	bound, err := p.targets.Resolve(ctx, TargetRequest{Environment: req.Environment, Binding: b})
	if err != nil {
		return plan.PlannedOperation{}, false, fmt.Errorf("planner: %s: %w", b, err)
	}
	// Released explicitly rather than by a deferred close inside the loop: the
	// targets of a ten-binding environment would otherwise all stay open until
	// the whole plan was finished, and a plugin-backed target is a subprocess.
	res, err := bound.Plan(ctx, targetv2.PlanRequest{Desired: desired})
	reversible := bound.Can(targetv2.CapabilityNativeRollback)
	closeErr := bound.Close()

	switch {
	case err != nil:
		return plan.PlannedOperation{}, false, fmt.Errorf("planner: %s: %w", b, err)
	case closeErr != nil:
		return plan.PlannedOperation{}, false, fmt.Errorf("planner: %s: releasing the target: %w", b, closeErr)
	case len(res.Blockers) > 0:
		// Checked before Changes: a target reporting no work and a reason the
		// apply would fail is describing a broken world rather than an idle
		// one, and skipping it would plan around the problem.
		return plan.PlannedOperation{}, false, fmt.Errorf("%w: %s: %s",
			ErrBlocked, b, strings.Join(res.Blockers, "; "))
	case !res.Changes:
		return plan.PlannedOperation{}, false, nil
	}

	return plan.PlannedOperation{
		// The binding name identifies the operation because an environment
		// already refuses two bindings sharing one, and because a name an
		// operator chose reads better in a plan than a generated id.
		ID:      plan.OperationID(b.Name),
		Target:  b.Name,
		Kind:    plan.OperationApply,
		Summary: fmt.Sprintf("deploy %s to %s", req.Release.Version, b),
		// Reversible is the target's own claim, not the plan's arrangement:
		// it says this step can be undone in place. A target without native
		// rollback is returned to its previous state by rendering and applying
		// the previous release, which is a different plan than this one.
		Reversible: reversible,
	}, true, nil
}

// rollback records where a failed deployment would return to.
//
// A first deployment gets none. There is no release to put back, and claiming a
// way back that does not exist is worse than admitting there is none.
func rollback(req deploy.PlanRequest, ops []plan.PlannedOperation) plan.RollbackPlan {
	if req.Current == nil || req.Current.ReleaseID == req.Release.ID {
		return plan.RollbackPlan{}
	}
	automatic := true
	for _, o := range ops {
		automatic = automatic && o.Reversible
	}
	return plan.RollbackPlan{
		FromRelease: req.Release.ID,
		ToRelease:   req.Current.ReleaseID,
		// Automatic only when every target can undo its own step. One that
		// cannot needs the previous release rendered and applied again, and
		// this plan does not carry the operations to do that — so the way back
		// exists but is not something the engine can take unattended.
		Automatic: automatic,
	}
}
