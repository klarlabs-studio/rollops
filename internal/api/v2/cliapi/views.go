package cliapi

import (
	"encoding/json"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/domain/deployment"
)

// The wire shape is declared here rather than inherited. The view types in
// api/v2 carry no json tags on purpose, for the reason httpapi gives: tagging
// them would make every field rename a breaking API change decided in the
// wrong package. Spec 26.3 makes this document contract for whatever is
// piping it into jq, so the names are spelled out where they are read.
//
// It repeats mcpapi's shapes closely and is not shared with them. Two
// transports that happen to agree today are still two contracts, and the day
// one of them has to add a field is the day sharing would force it on the
// other.

// PrincipalView is who did something. It carries no claims, and never will:
// INV-012 keeps them out of the view this projects.
type PrincipalView struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name,omitempty"`
}

func principal(p apiv2.Principal) PrincipalView {
	return PrincipalView{ID: p.ID, Type: p.Type, DisplayName: p.DisplayName}
}

// ProjectView is a project as a script reads it.
type ProjectView struct {
	ProjectID   string            `json:"project_id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Revision    uint64            `json:"revision"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

func project(p apiv2.Project) ProjectView {
	return ProjectView{
		ProjectID:   p.ID,
		Name:        p.Name,
		Description: p.Description,
		Labels:      p.Labels,
		Revision:    p.Revision,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

// TargetView names a substrate an environment deploys to.
//
// ConfigKeys are names, never values — what they resolve to is a credential as
// often as not (INV-012). The names still answer the question worth asking:
// whether this environment configures the thing the operator expected.
type TargetView struct {
	Name       string            `json:"name"`
	Driver     string            `json:"driver"`
	ConfigKeys []string          `json:"config_keys,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// PolicyBindingView is a policy in force for an environment.
type PolicyBindingView struct {
	Name string `json:"name"`
	Ref  string `json:"ref,omitempty"`
	Mode string `json:"mode,omitempty"`
}

// LifecycleView is how long an environment is meant to last. The TTL is
// rendered as seconds rather than Go's duration string, because a consumer
// that has to parse "1h30m0s" is parsing prose.
type LifecycleView struct {
	TTLSeconds    float64 `json:"ttl_seconds,omitempty"`
	DeleteOnClose bool    `json:"delete_on_close,omitempty"`
}

// EnvironmentView is an environment as a script reads it.
type EnvironmentView struct {
	EnvironmentID string              `json:"environment_id"`
	ProjectID     string              `json:"project_id"`
	Name          string              `json:"name"`
	Kind          string              `json:"kind"`
	Targets       []TargetView        `json:"targets,omitempty"`
	Policies      []PolicyBindingView `json:"policies,omitempty"`

	// VariableNames are names for the same reason ConfigKeys are.
	VariableNames []string `json:"variable_names,omitempty"`

	Labels    map[string]string `json:"labels,omitempty"`
	Lifecycle LifecycleView     `json:"lifecycle,omitempty"`
	Revision  uint64            `json:"revision"`
}

func environment(e apiv2.Environment) EnvironmentView {
	return EnvironmentView{
		EnvironmentID: e.ID,
		ProjectID:     e.ProjectID,
		Name:          e.Name,
		Kind:          e.Kind,
		Targets: mapped(e.Targets, func(t apiv2.TargetBinding) TargetView {
			return TargetView{Name: t.Name, Driver: t.Driver, ConfigKeys: t.Config, Labels: t.Labels}
		}),
		Policies: mapped(e.Policies, func(p apiv2.PolicyBinding) PolicyBindingView {
			return PolicyBindingView{Name: p.Name, Ref: p.Ref, Mode: p.Mode}
		}),
		VariableNames: e.Variables,
		Labels:        e.Labels,
		Lifecycle: LifecycleView{
			TTLSeconds:    e.Lifecycle.TTL.Seconds(),
			DeleteOnClose: e.Lifecycle.DeleteOnClose,
		},
		Revision: e.Revision,
	}
}

// SourceView is where the code a release was built from came from.
type SourceView struct {
	Provider   string `json:"provider,omitempty"`
	Repository string `json:"repository,omitempty"`
	Revision   string `json:"revision,omitempty"`
	Ref        string `json:"ref,omitempty"`
	TreeDigest string `json:"tree_digest,omitempty"`
	URL        string `json:"url,omitempty"`
}

func source(s apiv2.Source) SourceView {
	return SourceView{
		Provider:   s.Provider,
		Repository: s.Repository,
		Revision:   s.Revision,
		Ref:        s.Ref,
		TreeDigest: s.TreeDigest,
		URL:        s.URL,
	}
}

// DocumentView points at provenance, an SBOM or a signature. It is a
// reference: the document lives wherever it was published (ADR-0004).
type DocumentView struct {
	Kind    string `json:"kind,omitempty"`
	Format  string `json:"format,omitempty"`
	Locator string `json:"locator,omitempty"`
	Digest  string `json:"digest,omitempty"`
}

func document(d apiv2.DocumentRef) DocumentView {
	return DocumentView{Kind: d.Kind, Format: d.Format, Locator: d.Locator, Digest: d.Digest}
}

// ReleaseArtifactView binds one artifact to the role it plays in a release.
type ReleaseArtifactView struct {
	Role       string `json:"role"`
	ArtifactID string `json:"artifact_id"`
}

// ReleaseView is a release as a script reads it.
type ReleaseView struct {
	ReleaseID   string                `json:"release_id"`
	ProjectID   string                `json:"project_id"`
	Version     string                `json:"version"`
	Artifacts   []ReleaseArtifactView `json:"artifacts,omitempty"`
	Source      SourceView            `json:"source"`
	Provenance  DocumentView          `json:"provenance,omitempty"`
	Labels      map[string]string     `json:"labels,omitempty"`
	Annotations map[string]string     `json:"annotations,omitempty"`
	CreatedBy   PrincipalView         `json:"created_by"`
	CreatedAt   time.Time             `json:"created_at"`
}

func release(r apiv2.Release) ReleaseView {
	return ReleaseView{
		ReleaseID: r.ID,
		ProjectID: r.ProjectID,
		Version:   r.Version,
		Artifacts: mapped(r.Artifacts, func(a apiv2.ReleaseArtifact) ReleaseArtifactView {
			return ReleaseArtifactView{Role: a.Role, ArtifactID: a.ArtifactID}
		}),
		Source:      source(r.Source),
		Provenance:  document(r.Provenance),
		Labels:      r.Labels,
		Annotations: r.Annotations,
		CreatedBy:   principal(r.CreatedBy),
		CreatedAt:   r.CreatedAt,
	}
}

// ArtifactView is a built thing as a script reads it. The content is not here
// and never will be: what is stored is metadata (ADR-0004).
type ArtifactView struct {
	ArtifactID string            `json:"artifact_id"`
	ProjectID  string            `json:"project_id"`
	Kind       string            `json:"kind"`
	Digest     string            `json:"digest"`
	Locator    string            `json:"locator,omitempty"`
	Size       int64             `json:"size,omitempty"`
	MediaType  string            `json:"media_type,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	Provenance DocumentView      `json:"provenance,omitempty"`
	SBOMs      []DocumentView    `json:"sboms,omitempty"`
	Signatures []DocumentView    `json:"signatures,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
}

func artifact(a apiv2.Artifact) ArtifactView {
	return ArtifactView{
		ArtifactID: a.ID,
		ProjectID:  a.ProjectID,
		Kind:       a.Kind,
		Digest:     a.Digest,
		Locator:    a.Locator,
		Size:       a.Size,
		MediaType:  a.MediaType,
		Metadata:   a.Metadata,
		Provenance: document(a.Provenance),
		SBOMs:      mapped(a.SBOMs, document),
		Signatures: mapped(a.Signatures, document),
		CreatedAt:  a.CreatedAt,
	}
}

// ReasonView explains part of a decision. The code is for branching on, the
// message for a person to read.
type ReasonView struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// RequirementView is a condition that must hold before an apply may proceed.
type RequirementView struct {
	Type   string `json:"type"`
	Role   string `json:"role,omitempty"`
	Count  int    `json:"count,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// RiskView is how dangerous the change was judged to be.
type RiskView struct {
	Level   string       `json:"level"`
	Score   float64      `json:"score"`
	Factors []ReasonView `json:"factors,omitempty"`
}

// PolicyView is the verdict. Risk is nested inside it rather than beside it
// because that is how api/v2 holds it, and for the reason it gives: one answer
// to read rather than two that can disagree.
type PolicyView struct {
	Allowed      bool              `json:"allowed"`
	Requirements []RequirementView `json:"requirements,omitempty"`
	Reasons      []ReasonView      `json:"reasons,omitempty"`
	Risk         RiskView          `json:"risk"`
}

func decision(d apiv2.Decision) PolicyView {
	return PolicyView{
		Allowed: d.Allowed,
		Requirements: mapped(d.Requirements, func(r apiv2.Requirement) RequirementView {
			return RequirementView{Type: r.Type, Role: r.Role, Count: r.Count, Detail: r.Detail}
		}),
		Reasons: mapped(d.Reasons, func(r apiv2.Reason) ReasonView {
			return ReasonView{Code: r.Code, Message: r.Message}
		}),
		Risk: RiskView{
			Level: d.Risk.Level,
			Score: d.Risk.Score,
			Factors: mapped(d.Risk.Factors, func(f apiv2.RiskFactor) ReasonView {
				return ReasonView{Code: f.Code, Message: f.Message}
			}),
		},
	}
}

// ChangeView is one field a planned operation would alter.
//
// A sensitive change arrives with both values blank and the flag set. The row
// is kept rather than dropped so that nobody mistakes a withheld value for a
// field that did not change.
type ChangeView struct {
	Path      string `json:"path"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

// OperationView is one unit of work a plan would carry out.
type OperationView struct {
	ID           string       `json:"id"`
	Target       string       `json:"target"`
	Kind         string       `json:"kind"`
	Summary      string       `json:"summary,omitempty"`
	Changes      []ChangeView `json:"changes,omitempty"`
	Dependencies []string     `json:"dependencies,omitempty"`
	Reversible   bool         `json:"reversible"`
}

func operation(o apiv2.PlannedOperation) OperationView {
	return OperationView{
		ID:      o.ID,
		Target:  o.Target,
		Kind:    o.Kind,
		Summary: o.Summary,
		Changes: mapped(o.Changes, func(c apiv2.Change) ChangeView {
			return ChangeView{Path: c.Path, From: c.From, To: c.To, Sensitive: c.Sensitive}
		}),
		Dependencies: o.Dependencies,
		Reversible:   o.Reversible,
	}
}

// RollbackView is how to get back to where the environment was.
type RollbackView struct {
	FromRelease string          `json:"from_release_id,omitempty"`
	ToRelease   string          `json:"to_release_id,omitempty"`
	Operations  []OperationView `json:"operations,omitempty"`
	Automatic   bool            `json:"automatic"`
}

// PlanView is a plan as a script reads it.
type PlanView struct {
	PlanID        string `json:"plan_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ReleaseID     string `json:"release_id"`

	// BaseRevision is the environment revision the plan was computed against,
	// and Hash is what an approval binds to. Both are here so that a script can
	// say which plan it read rather than finding out from a refused apply.
	BaseRevision uint64 `json:"base_revision"`
	Hash         string `json:"hash,omitempty"`

	Strategy   string          `json:"strategy"`
	Operations []OperationView `json:"operations,omitempty"`
	Rollback   RollbackView    `json:"rollback"`

	Policy           PolicyView `json:"policy"`
	ApprovalRequired bool       `json:"approval_required"`
	NextActions      []string   `json:"next_actions,omitempty"`

	CreatedBy PrincipalView `json:"created_by"`
	CreatedAt time.Time     `json:"created_at"`
	ExpiresAt time.Time     `json:"expires_at"`
}

func plan(p apiv2.Plan) PlanView {
	return PlanView{
		PlanID:        p.ID,
		ProjectID:     p.ProjectID,
		EnvironmentID: p.EnvironmentID,
		ReleaseID:     p.ReleaseID,
		BaseRevision:  p.BaseRevision,
		Hash:          p.Hash,
		Strategy:      p.Strategy,
		Operations:    mapped(p.Operations, operation),
		Rollback: RollbackView{
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

// DeploymentView is a deployment as a script reads it.
type DeploymentView struct {
	DeploymentID  string `json:"deployment_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ReleaseID     string `json:"release_id"`
	PlanID        string `json:"plan_id,omitempty"`

	Strategy string `json:"strategy"`
	Status   string `json:"status"`

	// Terminal saves a script from keeping its own list of which statuses end a
	// deployment, which would go stale the first time one is added.
	Terminal bool `json:"terminal"`

	// NextActions are the commands that would be accepted now, derived from the
	// same transition table the engine enforces.
	NextActions []string `json:"next_actions,omitempty"`

	TriggerType   string `json:"trigger_type,omitempty"`
	TriggerDetail string `json:"trigger_detail,omitempty"`

	Actor PrincipalView `json:"actor"`

	// Previous is the deployment this one replaces, empty for the first.
	Previous string `json:"previous_deployment_id,omitempty"`

	Revision   uint64     `json:"revision"`
	CreatedAt  time.Time  `json:"created_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

func deploymentOf(d apiv2.Deployment) DeploymentView {
	return DeploymentView{
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

// MeasurementView is one figure a verifier read.
type MeasurementView struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

// EvidenceView points at what a verifier looked at.
type EvidenceView struct {
	Kind string `json:"kind"`
	URI  string `json:"uri"`
}

// CheckView is one verifier's answer.
type CheckView struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind,omitempty"`
	Version      string            `json:"version,omitempty"`
	Verdict      string            `json:"verdict"`
	Measurements []MeasurementView `json:"measurements,omitempty"`
	Reason       string            `json:"reason,omitempty"`
	Evidence     []EvidenceView    `json:"evidence,omitempty"`
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at"`
}

// VerificationRunView is what the checks concluded.
type VerificationRunView struct {
	RunID        string        `json:"run_id"`
	DeploymentID string        `json:"deployment_id"`
	PlanID       string        `json:"plan_id,omitempty"`
	Verdict      string        `json:"verdict"`
	Checks       []CheckView   `json:"checks,omitempty"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at"`
	Actor        PrincipalView `json:"actor"`
}

func run(r apiv2.VerificationRun) VerificationRunView {
	return VerificationRunView{
		RunID:        r.ID,
		DeploymentID: r.DeploymentID,
		PlanID:       r.PlanID,
		Verdict:      r.Verdict,
		Checks: mapped(r.Checks, func(c apiv2.Check) CheckView {
			return CheckView{
				Name:    c.Verifier.Name,
				Kind:    c.Verifier.Kind,
				Version: c.Verifier.Version,
				Verdict: c.Verdict,
				Measurements: mapped(c.Measurements, func(m apiv2.Measurement) MeasurementView {
					return MeasurementView{Name: m.Name, Value: m.Value}
				}),
				Reason: c.Reason,
				Evidence: mapped(c.Evidence, func(e apiv2.EvidenceRef) EvidenceView {
					return EvidenceView{Kind: e.Kind, URI: e.URI}
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

// EventView is one entry in a deployment's timeline.
//
// Payload is passed through as the log recorded it rather than flattened into
// named fields: §16.4 lets a reader tolerate a type and version it does not
// recognise, and a projection that named every payload here would have to be
// taught each new one before a script could see it at all.
type EventView struct {
	EventID       string          `json:"event_id"`
	Type          string          `json:"type"`
	Version       int             `json:"version"`
	Sequence      uint64          `json:"sequence"`
	Time          time.Time       `json:"time"`
	Principal     PrincipalView   `json:"principal"`
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

func event(e apiv2.Event) EventView {
	return EventView{
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
