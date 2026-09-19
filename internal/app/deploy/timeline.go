package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

// Payloads carry references and codes, never the prose written for a person.
// A code is what a query matches on and what survives translation; a message is
// wording, and rewording it would silently rewrite history in a log nothing may
// rewrite (INV-013). The aggregates still hold the full records, so nothing is
// lost — the timeline says which plan, and the plan says the rest.

type planCreated struct {
	ProjectID     identity.ProjectID     `json:"project_id"`
	EnvironmentID identity.EnvironmentID `json:"environment_id"`
	ReleaseID     identity.ReleaseID     `json:"release_id"`
	BaseRevision  identity.Revision      `json:"base_revision"`
	Strategy      deployment.Strategy    `json:"strategy"`
	Operations    int                    `json:"operations"`
	ExpiresAt     time.Time              `json:"expires_at"`
	Hash          string                 `json:"hash"`
}

type policyEvaluated struct {
	Allowed      bool             `json:"allowed"`
	RiskLevel    policy.RiskLevel `json:"risk_level"`
	RiskScore    float64          `json:"risk_score"`
	Requirements []string         `json:"requirements,omitempty"`
	Reasons      []string         `json:"reasons,omitempty"`
	Factors      []string         `json:"factors,omitempty"`
}

// deploymentAdmitted describes a deployment the engine may now pick up. The
// trigger is its type only: the detail beside it is whatever an operator typed
// on the command line, which is the one field in this package a credential
// could plausibly arrive in.
type deploymentAdmitted struct {
	PlanID        identity.PlanID        `json:"plan_id"`
	EnvironmentID identity.EnvironmentID `json:"environment_id"`
	ReleaseID     identity.ReleaseID     `json:"release_id"`
	Strategy      deployment.Strategy    `json:"strategy"`
	Trigger       deployment.TriggerType `json:"trigger"`
	Previous      *identity.DeploymentID `json:"previous,omitempty"`
	Requirements  []string               `json:"requirements,omitempty"`
}

// recordPlan writes the two events a plan produces. The created event is the
// root of the intent — everything an operator later asks about this deployment
// hangs off its correlation id — and the evaluation is caused by it rather than
// standing alone, because policy ruled on this plan and not on the world.
//
// They are two events and not one because they answer different questions and
// age differently. What was planned is fixed; why it was allowed is what an
// audit re-reads, and a re-evaluation at apply time appends another of the
// second kind without pretending to have re-created the plan.
func (s *Service) recordPlan(ctx context.Context, by identity.Principal, p plan.DeploymentPlan) error {
	payload, err := encode(planCreated{
		ProjectID:     p.ProjectID,
		EnvironmentID: p.EnvironmentID,
		ReleaseID:     p.ReleaseID,
		BaseRevision:  p.BaseRevision,
		Strategy:      p.Strategy,
		Operations:    len(p.Operations),
		ExpiresAt:     p.ExpiresAt,
		Hash:          p.Hash.String(),
	})
	if err != nil {
		return err
	}
	root, err := s.append(ctx, by, event.Event{
		Type:          event.DeploymentPlanCreated,
		AggregateType: event.AggregatePlan,
		AggregateID:   string(p.ID),
		Payload:       payload,
	})
	if err != nil {
		return err
	}

	verdict, err := encode(policyEvaluated{
		Allowed:      p.Policy.Allowed,
		RiskLevel:    p.Policy.Risk.Level,
		RiskScore:    p.Policy.Risk.Score,
		Requirements: requirementCodes(p.Policy.Requirements),
		Reasons:      reasonCodes(p.Policy.Reasons),
		Factors:      factorCodes(p.Policy.Risk.Factors),
	})
	if err != nil {
		return err
	}
	_, err = s.append(ctx, by, event.Event{
		Type:          event.PolicyEvaluated,
		AggregateType: event.AggregatePlan,
		AggregateID:   string(p.ID),
		CorrelationID: root.CorrelationID,
		CausationID:   root.ID,
		Payload:       verdict,
	})
	return err
}

// recordAdmission says what became of the apply. The status decides the type
// rather than the other way round, so a deployment parked for approval and one
// handed to the engine are told apart in the timeline the same way they are in
// the aggregate — and an approver finds out there is a gate by the same means
// an operator finds out there is not.
func (s *Service) recordAdmission(
	ctx context.Context,
	cmd ApplyCommand,
	p plan.DeploymentPlan,
	d deployment.Deployment,
	status deployment.Status,
) error {
	payload, err := encode(deploymentAdmitted{
		PlanID:        p.ID,
		EnvironmentID: d.EnvironmentID,
		ReleaseID:     d.ReleaseID,
		Strategy:      d.Strategy,
		Trigger:       cmd.Trigger.Type,
		Previous:      d.Previous,
		Requirements:  requirementCodes(p.Policy.Requirements),
	})
	if err != nil {
		return err
	}
	typ := event.DeploymentQueued
	if status == deployment.StatusAwaitingApproval {
		typ = event.DeploymentApprovalRequested
	}
	correlation, causation, err := s.intent(ctx, p.ID)
	if err != nil {
		return err
	}
	_, err = s.append(ctx, cmd.Actor, event.Event{
		Type:          typ,
		AggregateType: event.AggregateDeployment,
		AggregateID:   string(d.ID),
		CorrelationID: correlation,
		CausationID:   causation,
		Payload:       payload,
	})
	return err
}

// intent finds the event that started the chain this plan belongs to, so that
// planning and applying read as one intent however far apart they happened
// (§16.4). It is read from the log rather than carried in memory because an
// apply may arrive hours later, in another process, from another transport.
//
// A plan with no events is not a failure. Plans stored before the log existed
// still apply, and an apply that starts its own chain is a shorter timeline
// rather than a broken one — refusing here would make the log a precondition
// for deploying, which inverts what it is for.
func (s *Service) intent(
	ctx context.Context, id identity.PlanID,
) (correlation, causation identity.EventID, err error) {
	es, err := s.cfg.Events.ForAggregate(ctx, event.AggregatePlan, string(id), port.Page{Limit: 1})
	if err != nil {
		return "", "", fmt.Errorf("deploy: reading the plan timeline: %w", err)
	}
	if len(es) == 0 {
		return "", "", nil
	}
	return es[0].CorrelationID, es[0].ID, nil
}

// append stamps and writes one event. It exists so that no call site can forget
// to run an envelope through event.New, which is where attribution and
// redaction are applied (INV-005, INV-012).
func (s *Service) append(
	ctx context.Context, by identity.Principal, e event.Event,
) (event.Event, error) {
	stamped, err := event.New(s.cfg.IDs, s.cfg.Clock, by, e)
	if err != nil {
		return event.Event{}, fmt.Errorf("deploy: recording %s: %w", e.Type, err)
	}
	appended, err := s.cfg.Events.Append(ctx, stamped)
	if err != nil {
		return event.Event{}, fmt.Errorf("deploy: recording %s: %w", e.Type, err)
	}
	return appended, nil
}

func encode(v any) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("deploy: encoding event payload: %w", err)
	}
	return raw, nil
}

func requirementCodes(rs []policy.Requirement) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r.Type)
	}
	return out
}

func reasonCodes(rs []policy.Reason) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Code
	}
	return out
}

func factorCodes(fs []policy.RiskFactor) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Code
	}
	return out
}
