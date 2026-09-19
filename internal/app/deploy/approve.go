package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

var (
	// ErrNotAwaitingApproval reports an approval offered for a deployment that
	// is not at the gate. Past that point the work has begun, and recording
	// consent for it after the fact would describe a review that never
	// happened.
	ErrNotAwaitingApproval = errors.New("deploy: deployment is not awaiting approval")

	// ErrUnboundApproval reports an approval that does not say which revision
	// it is about. §13 requires the binding, so an approval without one is
	// refused rather than bound to whatever happens to be stored.
	ErrUnboundApproval = errors.New("deploy: approval does not state the revision it approves")
)

// subjectPlan is the kind every deployment approval is recorded against.
// Approvals bind to the plan rather than to the deployment because the plan is
// what an approver read and what its hash covers; a deployment changes as it
// runs, and an approval of a moving thing is an approval of nothing.
const subjectPlan = "plan"

// planSubject names the plan an approval is about. Apply and Approve build it
// the same way on purpose: a reference the two disagreed on would let one
// record approvals the other could never find.
func planSubject(p plan.DeploymentPlan) policy.SubjectRef {
	return policy.SubjectRef{Kind: subjectPlan, ID: string(p.ID), Revision: p.Hash.String()}
}

// ApproveCommand is one principal's answer about one plan.
//
// Revision is required. It is the hash of the plan the approver actually read,
// and stating it is what makes the approval bindable: if the stored plan is a
// different one, the approver is vouching for something that is not what would
// run, and the command is refused rather than silently rebound.
type ApproveCommand struct {
	DeploymentID identity.DeploymentID
	Revision     digest.Digest
	Decision     policy.ApprovalDecision
	Reason       string
	Actor        identity.Principal
}

// Approve records an answer about a gated deployment and moves it if the
// answer settles the matter.
//
// Granting does not queue the deployment on its own — the decision's
// requirements do. One approval against a two-approval requirement records the
// answer and leaves the gate shut, which is why the deployment is returned
// rather than a bare error: the caller needs to see whether it moved.
//
// Denying is decisive. A refusal is an answer, not a pause, so the deployment
// is cancelled rather than left waiting for somebody to overrule it.
func (s *Service) Approve(ctx context.Context, cmd ApproveCommand) (deployment.Deployment, error) {
	if cmd.Revision.IsZero() {
		return deployment.Deployment{}, ErrUnboundApproval
	}
	d, err := s.cfg.Deployments.Get(ctx, cmd.DeploymentID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf("deploy: deployment %s: %w", cmd.DeploymentID, err)
	}
	if d.Status != deployment.StatusAwaitingApproval {
		return deployment.Deployment{}, fmt.Errorf("%w: %s is %s", ErrNotAwaitingApproval, d.ID, d.Status)
	}
	p, err := s.cfg.Plans.Get(ctx, d.PlanID)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf("deploy: plan %s: %w", d.PlanID, err)
	}
	// The stored plan has to still be the plan before its hash means anything.
	if err := p.VerifyHash(); err != nil {
		return deployment.Deployment{}, err
	}
	if cmd.Revision != p.Hash {
		return deployment.Deployment{}, fmt.Errorf(
			"%w: approving %s, stored plan is %s", plan.ErrPlanStale, cmd.Revision, p.Hash)
	}

	subject := planSubject(p)
	id, err := identity.NewApprovalID(s.cfg.IDs)
	if err != nil {
		return deployment.Deployment{}, fmt.Errorf("deploy: approval id: %w", err)
	}
	approval := policy.Approval{
		ID:        id,
		Subject:   subject,
		Principal: cmd.Actor,
		Decision:  cmd.Decision,
		Reason:    cmd.Reason,
		CreatedAt: s.cfg.Clock.Now(),
	}
	if err := approval.Validate(); err != nil {
		return deployment.Deployment{}, err
	}

	var out deployment.Deployment
	err = s.cfg.Transactor.WithinTransaction(ctx, func(ctx context.Context) error {
		if err := s.cfg.Approvals.Create(ctx, approval); err != nil {
			return fmt.Errorf("deploy: storing the approval: %w", err)
		}
		// Read back every approval including the one just written, so the
		// requirement is judged against the whole record rather than against
		// this call. Two approvers answering at once both see both answers.
		recorded, err := s.cfg.Approvals.ListForSubject(ctx, subjectPlan, string(p.ID))
		if err != nil {
			return fmt.Errorf("deploy: reading the approvals: %w", err)
		}
		if err := s.recordAnswer(ctx, approval, p, d); err != nil {
			return err
		}

		next, err := resolve(p.Policy, subject, recorded, s.cfg.Clock.Now())
		if err != nil {
			return err
		}
		if next == d.Status {
			out = d
			return nil
		}
		moved, err := d.TransitionTo(next, s.cfg.Clock.Now())
		if err != nil {
			return err
		}
		rev, err := s.cfg.Deployments.Update(ctx, moved)
		if err != nil {
			return fmt.Errorf("deploy: storing the deployment: %w", err)
		}
		moved.Revision = rev
		// Only an admission gets its own event. A deployment stopped by a
		// refusal already has the refusal on its timeline, and saying it
		// failed as well would describe a run that never started.
		if next == deployment.StatusQueued {
			if err := s.recordGateCleared(ctx, cmd.Actor, p, moved); err != nil {
				return err
			}
		}
		out = moved
		return nil
	})
	if err != nil {
		return deployment.Deployment{}, err
	}
	return out, nil
}

// resolve reports the status the deployment should now be in. A denial
// cancels; a satisfied decision queues; anything still outstanding leaves the
// deployment where it is, because an unmet requirement is the gate waiting
// rather than a failure.
func resolve(
	d policy.Decision, subject policy.SubjectRef, approvals []policy.Approval, at time.Time,
) (deployment.Status, error) {
	err := d.SatisfiedBy(subject, approvals, at)
	switch {
	case err == nil:
		return deployment.StatusQueued, nil
	case errors.Is(err, policy.ErrApprovalDenied):
		return deployment.StatusCancelled, nil
	case errors.Is(err, policy.ErrRequirementUnmet):
		return deployment.StatusAwaitingApproval, nil
	default:
		return "", err
	}
}

// recordAnswer puts the approval itself on the deployment's timeline, whatever
// it decided. An operator asking why a deployment ran — or did not — reads the
// same log as for everything else that happened to it.
func (s *Service) recordAnswer(
	ctx context.Context, a policy.Approval, p plan.DeploymentPlan, d deployment.Deployment,
) error {
	payload, err := encode(approvalRecorded{
		ApprovalID: a.ID,
		PlanID:     p.ID,
		Revision:   a.Subject.Revision,
		Decision:   a.Decision,
		Reason:     a.Reason,
	})
	if err != nil {
		return err
	}
	typ := event.DeploymentApproved
	if a.Decision == policy.ApprovalDenied {
		typ = event.DeploymentApprovalRejected
	}
	correlation, causation, err := s.intent(ctx, p.ID)
	if err != nil {
		return err
	}
	_, err = s.append(ctx, a.Principal, event.Event{
		Type:          typ,
		AggregateType: event.AggregateDeployment,
		AggregateID:   string(d.ID),
		CorrelationID: correlation,
		CausationID:   causation,
		Payload:       payload,
	})
	return err
}

// recordGateCleared records the deployment moving off the gate, which is a
// different fact from any one person's answer: it is the moment the
// requirements stopped standing in the way.
func (s *Service) recordGateCleared(
	ctx context.Context,
	by identity.Principal,
	p plan.DeploymentPlan,
	d deployment.Deployment,
) error {
	payload, err := encode(deploymentAdmitted{
		PlanID:        p.ID,
		EnvironmentID: d.EnvironmentID,
		ReleaseID:     d.ReleaseID,
		Strategy:      d.Strategy,
		Trigger:       d.Trigger.Type,
		Previous:      d.Previous,
		Requirements:  requirementCodes(p.Policy.Requirements),
	})
	if err != nil {
		return err
	}
	correlation, causation, err := s.intent(ctx, p.ID)
	if err != nil {
		return err
	}
	_, err = s.append(ctx, by, event.Event{
		Type:          event.DeploymentQueued,
		AggregateType: event.AggregateDeployment,
		AggregateID:   string(d.ID),
		CorrelationID: correlation,
		CausationID:   causation,
		Payload:       payload,
	})
	return err
}
