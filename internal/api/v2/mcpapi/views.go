package mcpapi

import (
	"encoding/json"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/domain/deployment"
)

// The wire shape is declared here rather than inherited. The view types in
// api/v2 carry no json tags on purpose, for the reason httpapi gives: tagging
// them would make every field rename a breaking API change decided in the
// wrong package. Agents are the most literal readers this system has, so the
// names are spelled out where they are read.

// PrincipalOut is who did something. It carries no claims, and never will:
// INV-012 keeps them out of the view this projects.
type PrincipalOut struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name,omitempty"`
}

func principal(p apiv2.Principal) PrincipalOut {
	return PrincipalOut{ID: p.ID, Type: p.Type, DisplayName: p.DisplayName}
}

// ReasonOut explains part of a decision. The code is for branching on, the
// message for the model to read.
type ReasonOut struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// RequirementOut is a condition that must hold before an apply may proceed.
type RequirementOut struct {
	Type   string `json:"type"`
	Role   string `json:"role,omitempty"`
	Count  int    `json:"count,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// RiskOut is how dangerous the change was judged to be.
type RiskOut struct {
	Level   string      `json:"level"`
	Score   float64     `json:"score"`
	Factors []ReasonOut `json:"factors,omitempty"`
}

// PolicyOut is the verdict §25.2 asks every plan result to carry.
//
// Risk is nested inside it rather than beside it because that is how api/v2
// holds it, and for the reason it gives: one answer to read rather than two
// that can disagree. §25.2's example draws them as siblings, but it is an
// example of what must be present, not of what must be adjacent.
type PolicyOut struct {
	Allowed      bool             `json:"allowed"`
	Requirements []RequirementOut `json:"requirements,omitempty"`
	Reasons      []ReasonOut      `json:"reasons,omitempty"`
	Risk         RiskOut          `json:"risk"`
}

func decision(d apiv2.Decision) PolicyOut {
	out := PolicyOut{
		Allowed: d.Allowed,
		Risk: RiskOut{
			Level: d.Risk.Level,
			Score: d.Risk.Score,
			Factors: mapped(d.Risk.Factors, func(f apiv2.RiskFactor) ReasonOut {
				return ReasonOut{Code: f.Code, Message: f.Message}
			}),
		},
	}
	out.Requirements = mapped(d.Requirements, func(r apiv2.Requirement) RequirementOut {
		return RequirementOut{Type: r.Type, Role: r.Role, Count: r.Count, Detail: r.Detail}
	})
	out.Reasons = mapped(d.Reasons, func(r apiv2.Reason) ReasonOut {
		return ReasonOut{Code: r.Code, Message: r.Message}
	})
	return out
}

// ChangeOut is one field a planned operation would alter.
//
// A sensitive change arrives with both values blank and the flag set. The row
// is kept rather than dropped so that a model cannot mistake a withheld value
// for a field that did not change — and so it does not go looking for the
// value somewhere it might actually find it.
type ChangeOut struct {
	Path      string `json:"path"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// OperationOut is one unit of work a plan would carry out.
type OperationOut struct {
	ID           string      `json:"id"`
	Target       string      `json:"target"`
	Kind         string      `json:"kind"`
	Summary      string      `json:"summary,omitempty"`
	Changes      []ChangeOut `json:"changes,omitempty"`
	Dependencies []string    `json:"dependencies,omitempty"`
	Reversible   bool        `json:"reversible"`
}

func operation(o apiv2.PlannedOperation) OperationOut {
	return OperationOut{
		ID:      o.ID,
		Target:  o.Target,
		Kind:    o.Kind,
		Summary: o.Summary,
		Changes: mapped(o.Changes, func(c apiv2.Change) ChangeOut {
			return ChangeOut{Path: c.Path, From: c.From, To: c.To, Sensitive: c.Sensitive}
		}),
		Dependencies: o.Dependencies,
		Reversible:   o.Reversible,
	}
}

// RollbackOut is how to get back to where the environment was.
type RollbackOut struct {
	FromRelease string         `json:"from_release_id,omitempty"`
	ToRelease   string         `json:"to_release_id,omitempty"`
	Operations  []OperationOut `json:"operations,omitempty"`
	Automatic   bool           `json:"automatic"`
}

// PlanOut is a plan as an agent reads it.
type PlanOut struct {
	PlanID        string `json:"plan_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ReleaseID     string `json:"release_id"`

	// BaseRevision is the environment revision the plan was computed against.
	// It is here so an agent can tell for itself whether the plan it is holding
	// still describes the world, rather than finding out from a refused apply.
	BaseRevision uint64 `json:"base_revision"`

	// Hash is what an approval binds to. An agent asking a human to approve
	// something has to be able to say which plan it read.
	Hash string `json:"hash,omitempty"`

	Strategy   string         `json:"strategy"`
	Operations []OperationOut `json:"operations,omitempty"`
	Rollback   RollbackOut    `json:"rollback"`

	Policy           PolicyOut `json:"policy"`
	ApprovalRequired bool      `json:"approval_required"`
	NextActions      []string  `json:"next_actions,omitempty"`

	CreatedBy PrincipalOut `json:"created_by"`
	CreatedAt time.Time    `json:"created_at"`
	ExpiresAt time.Time    `json:"expires_at"`
}

func plan(p apiv2.Plan) PlanOut {
	return PlanOut{
		PlanID:        p.ID,
		ProjectID:     p.ProjectID,
		EnvironmentID: p.EnvironmentID,
		ReleaseID:     p.ReleaseID,
		BaseRevision:  p.BaseRevision,
		Hash:          p.Hash,
		Strategy:      p.Strategy,
		Operations:    mapped(p.Operations, operation),
		Rollback: RollbackOut{
			FromRelease: p.Rollback.FromRelease,
			ToRelease:   p.Rollback.ToRelease,
			Operations:  mapped(p.Rollback.Operations, operation),
			Automatic:   p.Rollback.Automatic,
		},
		Policy:           decision(p.Policy),
		ApprovalRequired: p.ApprovalRequired(),
		NextActions:      p.NextActions(),
		CreatedBy:        principal(p.CreatedBy),
		CreatedAt:        p.CreatedAt,
		ExpiresAt:        p.ExpiresAt,
	}
}

// DeploymentOut is a deployment as an agent reads it.
type DeploymentOut struct {
	DeploymentID  string `json:"deployment_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ReleaseID     string `json:"release_id"`
	PlanID        string `json:"plan_id,omitempty"`

	Strategy string `json:"strategy"`
	Status   string `json:"status"`

	// Terminal saves an agent from keeping its own list of which statuses end a
	// deployment, which would go stale the first time one is added.
	Terminal bool `json:"terminal"`

	// NextActions are the tools that would be accepted now, derived from the
	// same transition table the engine enforces.
	NextActions []string `json:"next_actions,omitempty"`

	TriggerType   string `json:"trigger_type,omitempty"`
	TriggerDetail string `json:"trigger_detail,omitempty"`

	Actor PrincipalOut `json:"actor"`

	// Previous is the deployment this one replaces, empty for the first. It is
	// what a rollback goes back to.
	Previous string `json:"previous_deployment_id,omitempty"`

	Revision   uint64     `json:"revision"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func deploymentOf(d apiv2.Deployment) DeploymentOut {
	return DeploymentOut{
		DeploymentID:  d.ID,
		ProjectID:     d.ProjectID,
		EnvironmentID: d.EnvironmentID,
		ReleaseID:     d.ReleaseID,
		PlanID:        d.PlanID,
		Strategy:      d.Strategy,
		Status:        d.Status,
		Terminal:      deployment.Status(d.Status).IsTerminal(),
		NextActions:   d.NextActions(),
		TriggerType:   d.Trigger.Type,
		TriggerDetail: d.Trigger.Detail,
		Actor:         principal(d.Actor),
		Previous:      d.Previous,
		Revision:      d.Revision,
		CreatedAt:     d.CreatedAt,
		StartedAt:     d.StartedAt,
		FinishedAt:    d.FinishedAt,
	}
}

// MeasurementOut is one figure a verifier read.
type MeasurementOut struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// EvidenceOut points at what a verifier looked at.
type EvidenceOut struct {
	Kind string `json:"kind"`
	URI  string `json:"uri"`
}

// CheckOut is one verifier's answer.
type CheckOut struct {
	Name         string           `json:"name"`
	Kind         string           `json:"kind,omitempty"`
	Version      string           `json:"version,omitempty"`
	Verdict      string           `json:"verdict"`
	Measurements []MeasurementOut `json:"measurements,omitempty"`
	Reason       string           `json:"reason,omitempty"`
	Evidence     []EvidenceOut    `json:"evidence,omitempty"`
	StartedAt    time.Time        `json:"started_at"`
	FinishedAt   time.Time        `json:"finished_at"`
}

// VerificationRunOut is what the checks concluded.
type VerificationRunOut struct {
	RunID        string       `json:"run_id"`
	DeploymentID string       `json:"deployment_id"`
	PlanID       string       `json:"plan_id,omitempty"`
	Verdict      string       `json:"verdict"`
	Checks       []CheckOut   `json:"checks,omitempty"`
	StartedAt    time.Time    `json:"started_at"`
	FinishedAt   time.Time    `json:"finished_at"`
	Actor        PrincipalOut `json:"actor"`
}

func run(r apiv2.VerificationRun) VerificationRunOut {
	return VerificationRunOut{
		RunID:        r.ID,
		DeploymentID: r.DeploymentID,
		PlanID:       r.PlanID,
		Verdict:      r.Verdict,
		Checks: mapped(r.Checks, func(c apiv2.Check) CheckOut {
			return CheckOut{
				Name:    c.Verifier.Name,
				Kind:    c.Verifier.Kind,
				Version: c.Verifier.Version,
				Verdict: c.Verdict,
				Measurements: mapped(c.Measurements, func(m apiv2.Measurement) MeasurementOut {
					return MeasurementOut{Name: m.Name, Value: m.Value}
				}),
				Reason: c.Reason,
				Evidence: mapped(c.Evidence, func(e apiv2.EvidenceRef) EvidenceOut {
					return EvidenceOut{Kind: e.Kind, URI: e.URI}
				}),
				StartedAt:  c.StartedAt,
				FinishedAt: c.FinishedAt,
			}
		}),
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
		Actor:      principal(r.Actor),
	}
}

// TargetOut names a substrate an environment deploys to.
//
// ConfigKeys are names, never values — what they resolve to is a credential as
// often as not, and §25.2 is explicit that using MCP does not entitle an agent
// to one. The names still answer the question worth asking: whether this
// environment configures the thing the agent expected.
type TargetOut struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	ConfigKeys []string          `json:"config_keys,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// PolicyBindingOut is a policy in force for an environment.
type PolicyBindingOut struct {
	Name string `json:"name"`
	Ref  string `json:"ref,omitempty"`
}

// EnvironmentOut is an environment as an agent reads it.
type EnvironmentOut struct {
	EnvironmentID string             `json:"environment_id"`
	ProjectID     string             `json:"project_id"`
	Name          string             `json:"name"`
	Kind          string             `json:"kind"`
	Targets       []TargetOut        `json:"targets,omitempty"`
	Policies      []PolicyBindingOut `json:"policies,omitempty"`

	// VariableNames are names for the same reason ConfigKeys are.
	VariableNames []string `json:"variable_names,omitempty"`

	Labels   map[string]string `json:"labels,omitempty"`
	Revision uint64            `json:"revision"`
}

func environment(e apiv2.Environment) EnvironmentOut {
	return EnvironmentOut{
		EnvironmentID: e.ID,
		ProjectID:     e.ProjectID,
		Name:          e.Name,
		Kind:          e.Kind,
		Targets: mapped(e.Targets, func(t apiv2.TargetBinding) TargetOut {
			return TargetOut{Name: t.Name, Driver: t.Driver, ConfigKeys: t.Config, Labels: t.Labels}
		}),
		Policies: mapped(e.Policies, func(p apiv2.PolicyBinding) PolicyBindingOut {
			return PolicyBindingOut{Name: p.Name, Ref: p.Ref}
		}),
		VariableNames: e.Variables,
		Labels:        e.Labels,
		Revision:      e.Revision,
	}
}

// SourceOut is the commit a release was built from.
type SourceOut struct {
	Provider   string `json:"provider,omitempty"`
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Ref        string `json:"ref,omitempty"`
}

// ReleaseArtifactOut binds one artifact to the role it plays in a release.
type ReleaseArtifactOut struct {
	Role       string `json:"role"`
	ArtifactID string `json:"artifact_id"`
}

// ReleaseOut is a release as an agent reads it.
type ReleaseOut struct {
	ReleaseID string               `json:"release_id"`
	ProjectID string               `json:"project_id"`
	Version   string               `json:"version"`
	Artifacts []ReleaseArtifactOut `json:"artifacts,omitempty"`
	Source    SourceOut            `json:"source"`
	Labels    map[string]string    `json:"labels,omitempty"`
	CreatedBy PrincipalOut         `json:"created_by"`
	CreatedAt time.Time            `json:"created_at"`
}

func release(r apiv2.Release) ReleaseOut {
	return ReleaseOut{
		ReleaseID: r.ID,
		ProjectID: r.ProjectID,
		Version:   r.Version,
		Artifacts: mapped(r.Artifacts, func(a apiv2.ReleaseArtifact) ReleaseArtifactOut {
			return ReleaseArtifactOut{Role: a.Role, ArtifactID: a.ArtifactID}
		}),
		Source: SourceOut{
			Provider:   r.Source.Provider,
			Repository: r.Source.Repository,
			Revision:   r.Source.Revision,
			Ref:        r.Source.Ref,
		},
		Labels:    r.Labels,
		CreatedBy: principal(r.CreatedBy),
		CreatedAt: r.CreatedAt,
	}
}

// EventOut is one entry in a deployment's timeline.
//
// Payload is passed through as the log recorded it rather than flattened into
// named fields: §16.4 lets a reader tolerate a type and version it does not
// recognise, and a projection that named every payload here would have to be
// taught each new one before an agent could see it at all.
type EventOut struct {
	EventID       string          `json:"event_id"`
	Type          string          `json:"type"`
	Version       int             `json:"version"`
	Sequence      uint64          `json:"sequence"`
	Time          time.Time       `json:"time"`
	Principal     PrincipalOut    `json:"principal"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

func event(e apiv2.Event) EventOut {
	return EventOut{
		EventID:       e.ID,
		Type:          e.Type,
		Version:       e.Version,
		Sequence:      e.Sequence,
		Time:          e.Time,
		Principal:     principal(e.Principal),
		CorrelationID: e.CorrelationID,
		CausationID:   e.CausationID,
		Payload:       e.Payload,
	}
}

// mapped projects a slice, returning nil for an empty one so that a field
// marked omitempty stays absent rather than arriving as [].
func mapped[A, B any](in []A, f func(A) B) []B {
	if len(in) == 0 {
		return nil
	}
	out := make([]B, 0, len(in))
	for _, a := range in {
		out = append(out, f(a))
	}
	return out
}
