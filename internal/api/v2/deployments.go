package apiv2

import (
	"context"
	"time"

	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

// Trigger is what caused a deployment.
type Trigger struct {
	Type   string
	Detail string
}

// Deployment is a deployment as the API describes it.
type Deployment struct {
	ID            string
	ProjectID     string
	EnvironmentID string
	ReleaseID     string
	PlanID        string
	Strategy      string
	Status        string
	Trigger       Trigger
	Actor         Principal
	CreatedAt     time.Time

	// StartedAt and FinishedAt are absent until they happen rather than zero,
	// so "not started" stays distinguishable from "started at the epoch".
	StartedAt  *time.Time
	FinishedAt *time.Time

	// Previous is the deployment this one replaces, empty for the first. It is
	// what makes a rollback target readable from the record.
	Previous string

	Revision uint64
}

// Change is one field a planned operation would alter.
//
// A change marked sensitive arrives with both values blank. Sensitive is kept
// rather than dropping the row, because a reader has to be able to tell a
// deliberate blank from a field that did not change.
type Change struct {
	Path      string
	From      string
	To        string
	Sensitive bool
}

// PlannedOperation is one unit of work a plan would carry out.
type PlannedOperation struct {
	ID           string
	Target       string
	Kind         string
	Summary      string
	Changes      []Change
	Dependencies []string
	Reversible   bool
}

// Reason explains part of a decision. The code is for matching, the message
// for the person reading the plan.
type Reason struct {
	Code    string
	Message string
}

// Requirement is a condition that must hold before an apply may proceed.
type Requirement struct {
	Type   string
	Role   string
	Count  int
	Detail string
}

// RiskFactor is one explainable contribution to a risk assessment.
type RiskFactor struct {
	Code    string
	Message string
}

// RiskAssessment is how dangerous the change was judged to be.
type RiskAssessment struct {
	Level   string
	Score   float64
	Factors []RiskFactor
}

// Decision is the authorization record for a plan. Risk is inside it rather
// than beside it, so there is one answer to read rather than two that can
// disagree.
type Decision struct {
	Allowed      bool
	Requirements []Requirement
	Reasons      []Reason
	Risk         RiskAssessment
}

// RollbackPlan is how to get back to where the environment was.
type RollbackPlan struct {
	FromRelease string
	ToRelease   string
	Operations  []PlannedOperation
	Automatic   bool
}

// Plan is a deployment plan as the API describes it.
type Plan struct {
	ID            string
	ProjectID     string
	EnvironmentID string
	ReleaseID     string

	// BaseRevision is the environment revision the plan was computed against.
	// Apply compares it with the live one rather than silently re-planning, so
	// a caller holding a plan can tell for itself whether it has gone stale.
	BaseRevision uint64

	Strategy   string
	Operations []PlannedOperation
	Policy     Decision
	Rollback   RollbackPlan
	CreatedBy  Principal
	CreatedAt  time.Time
	ExpiresAt  time.Time

	// Hash is what an approval binds to. It is here because an approver needs
	// to be able to say which plan they approved, and the id alone does not
	// prove the stored plan is still the one they read.
	Hash string
}

// GetDeploymentRequest names one deployment.
type GetDeploymentRequest struct{ ID string }

// GetDeployment returns one deployment.
func (s *Service) GetDeployment(ctx context.Context, req GetDeploymentRequest) (Deployment, error) {
	id, err := identity.ParseDeploymentID(req.ID)
	if err != nil {
		return Deployment{}, badArgument("apiv2: deployment id: %w", err)
	}
	d, err := s.deployments.Get(ctx, id)
	if err != nil {
		return Deployment{}, failure("apiv2: deployment %s: %w", id, err)
	}
	return viewDeployment(d), nil
}

// ListDeploymentsRequest asks for a page of one environment's deployments,
// most recent first.
type ListDeploymentsRequest struct {
	EnvironmentID string
	Page          page.Request
}

// ListDeploymentsResponse is a page of deployments.
type ListDeploymentsResponse struct {
	Deployments []Deployment
	Next        string
}

// ListDeployments returns a page of one environment's deployments.
func (s *Service) ListDeployments(ctx context.Context, req ListDeploymentsRequest) (ListDeploymentsResponse, error) {
	envID, err := identity.ParseEnvironmentID(req.EnvironmentID)
	if err != nil {
		return ListDeploymentsResponse{}, badArgument("apiv2: environment id: %w", err)
	}
	stored, err := s.deployments.ListForEnvironment(ctx, envID)
	if err != nil {
		return ListDeploymentsResponse{}, failure("apiv2: environment %s: deployments: %w", envID, err)
	}
	p, err := page.Of(stored, req.Page, func(d deployment.Deployment) string { return string(d.ID) })
	if err != nil {
		return ListDeploymentsResponse{}, failure("apiv2: environment %s: deployments: %w", envID, err)
	}
	return ListDeploymentsResponse{Deployments: mapped(p.Items, viewDeployment), Next: p.Next}, nil
}

// GetPlanRequest names one plan.
type GetPlanRequest struct{ ID string }

// GetPlan returns one plan.
//
// It does not verify the hash. A plan is also fetched to be explained, and
// refusing to render a tampered one would hide the evidence — the caller
// compares Hash against what it was told to expect.
func (s *Service) GetPlan(ctx context.Context, req GetPlanRequest) (Plan, error) {
	id, err := identity.ParsePlanID(req.ID)
	if err != nil {
		return Plan{}, badArgument("apiv2: plan id: %w", err)
	}
	p, err := s.plans.Get(ctx, id)
	if err != nil {
		return Plan{}, failure("apiv2: plan %s: %w", id, err)
	}
	// Redacted blanks the value of every change marked sensitive (INV-012).
	// It is applied here rather than trusted from storage: a plan reaches this
	// read from whatever wrote it, and a rendering path that assumed someone
	// upstream had redacted is a rendering path that eventually does not.
	return viewPlan(p.Redacted()), nil
}

func viewDeployment(d deployment.Deployment) Deployment {
	v := Deployment{
		ID:            string(d.ID),
		ProjectID:     string(d.ProjectID),
		EnvironmentID: string(d.EnvironmentID),
		ReleaseID:     string(d.ReleaseID),
		PlanID:        string(d.PlanID),
		Strategy:      string(d.Strategy),
		Status:        string(d.Status),
		Trigger:       Trigger{Type: string(d.Trigger.Type), Detail: d.Trigger.Detail},
		Actor:         viewPrincipal(d.Actor),
		CreatedAt:     d.CreatedAt,
		StartedAt:     copyTime(d.StartedAt),
		FinishedAt:    copyTime(d.FinishedAt),
		Revision:      uint64(d.Revision),
	}
	if d.Previous != nil {
		v.Previous = string(*d.Previous)
	}
	return v
}

func viewPlan(p plan.DeploymentPlan) Plan {
	return Plan{
		ID:            string(p.ID),
		ProjectID:     string(p.ProjectID),
		EnvironmentID: string(p.EnvironmentID),
		ReleaseID:     string(p.ReleaseID),
		BaseRevision:  uint64(p.BaseRevision),
		Strategy:      string(p.Strategy),
		Operations:    mapped(p.Operations, viewOperation),
		Policy:        viewDecision(p.Policy),
		Rollback: RollbackPlan{
			FromRelease: string(p.Rollback.FromRelease),
			ToRelease:   string(p.Rollback.ToRelease),
			Operations:  mapped(p.Rollback.Operations, viewOperation),
			Automatic:   p.Rollback.Automatic,
		},
		CreatedBy: viewPrincipal(p.CreatedBy),
		CreatedAt: p.CreatedAt,
		ExpiresAt: p.ExpiresAt,
		Hash:      p.Hash.String(),
	}
}

func viewOperation(o plan.PlannedOperation) PlannedOperation {
	v := PlannedOperation{
		ID:         string(o.ID),
		Target:     o.Target,
		Kind:       string(o.Kind),
		Summary:    o.Summary,
		Changes:    mapped(o.Diff.Changes, viewChange),
		Reversible: o.Reversible,
	}
	for _, d := range o.Dependencies {
		v.Dependencies = append(v.Dependencies, string(d))
	}
	return v
}

func viewChange(c plan.Change) Change {
	return Change{Path: c.Path, From: c.From, To: c.To, Sensitive: c.Sensitive}
}

func viewDecision(d policy.Decision) Decision {
	return Decision{
		Allowed:      d.Allowed,
		Requirements: mapped(d.Requirements, viewRequirement),
		Reasons:      mapped(d.Reasons, viewReason),
		Risk: RiskAssessment{
			Level:   string(d.Risk.Level),
			Score:   d.Risk.Score,
			Factors: mapped(d.Risk.Factors, viewRiskFactor),
		},
	}
}

func viewRequirement(r policy.Requirement) Requirement {
	return Requirement{Type: string(r.Type), Role: r.Role, Count: r.Count, Detail: r.Detail}
}

func viewReason(r policy.Reason) Reason {
	return Reason{Code: r.Code, Message: r.Message}
}

func viewRiskFactor(f policy.RiskFactor) RiskFactor {
	return RiskFactor{Code: f.Code, Message: f.Message}
}

// copyTime returns a copy rather than the stored pointer, so that a caller
// mutating the value it was handed cannot reach into the aggregate behind it.
func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}
