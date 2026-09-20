// The JSON a caller reads.
//
// These types are the published shape. They are separate from the view types in
// api/v2 so that the two can be changed independently: renaming a view field is
// an internal edit, where renaming a field here is a breaking API change that
// has to be decided as one. Nothing here holds a value a view withheld — the
// views already answer INV-012 by naming configuration keys rather than what
// they resolve to — and nothing here adds a field the service did not provide.
//
// Times that may not have happened are pointers, so a deployment that has not
// started says so by omission rather than by claiming the epoch.
package httpapi

import (
	"encoding/json"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
)

type principal struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name,omitempty"`
}

func wirePrincipal(p apiv2.Principal) principal {
	return principal{ID: p.ID, Type: p.Type, DisplayName: p.DisplayName}
}

type trigger struct {
	Type   string `json:"type"`
	Detail string `json:"detail,omitempty"`
}

type deployment struct {
	ID            string     `json:"id"`
	ProjectID     string     `json:"project_id"`
	EnvironmentID string     `json:"environment_id"`
	ReleaseID     string     `json:"release_id"`
	PlanID        string     `json:"plan_id"`
	Strategy      string     `json:"strategy"`
	Status        string     `json:"status"`
	Trigger       trigger    `json:"trigger"`
	Actor         principal  `json:"actor"`
	CreatedAt     time.Time  `json:"created_at"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Previous      string     `json:"previous,omitempty"`

	// Revision is what an optimistic write would be checked against. It is
	// published because a caller that cannot read it cannot detect that the
	// deployment moved under it.
	Revision uint64 `json:"revision"`
}

func wireDeployment(d apiv2.Deployment) deployment {
	return deployment{
		ID:            d.ID,
		ProjectID:     d.ProjectID,
		EnvironmentID: d.EnvironmentID,
		ReleaseID:     d.ReleaseID,
		PlanID:        d.PlanID,
		Strategy:      d.Strategy,
		Status:        d.Status,
		Trigger:       trigger{Type: d.Trigger.Type, Detail: d.Trigger.Detail},
		Actor:         wirePrincipal(d.Actor),
		CreatedAt:     d.CreatedAt,
		StartedAt:     d.StartedAt,
		FinishedAt:    d.FinishedAt,
		Previous:      d.Previous,
		Revision:      d.Revision,
	}
}

type change struct {
	Path string `json:"path"`

	// From and To are both blank on a sensitive change, and Sensitive is what
	// tells a reader that apart from a field that did not change. The row is
	// kept rather than dropped for exactly that reason.
	From      string `json:"from"`
	To        string `json:"to"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type plannedOperation struct {
	ID           string   `json:"id"`
	Target       string   `json:"target"`
	Kind         string   `json:"kind"`
	Summary      string   `json:"summary,omitempty"`
	Changes      []change `json:"changes"`
	Dependencies []string `json:"dependencies"`
	Reversible   bool     `json:"reversible"`
}

type reason struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type requirement struct {
	Type   string `json:"type"`
	Role   string `json:"role,omitempty"`
	Count  int    `json:"count,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type riskAssessment struct {
	Level   string   `json:"level"`
	Score   float64  `json:"score"`
	Factors []reason `json:"factors"`
}

type decision struct {
	Allowed      bool           `json:"allowed"`
	Requirements []requirement  `json:"requirements"`
	Reasons      []reason       `json:"reasons"`
	Risk         riskAssessment `json:"risk"`
}

type rollbackPlan struct {
	FromRelease string             `json:"from_release,omitempty"`
	ToRelease   string             `json:"to_release,omitempty"`
	Operations  []plannedOperation `json:"operations"`
	Automatic   bool               `json:"automatic"`
}

type plan struct {
	ID            string `json:"id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`
	ReleaseID     string `json:"release_id"`
	BaseRevision  uint64 `json:"base_revision"`

	Strategy   string             `json:"strategy"`
	Operations []plannedOperation `json:"operations"`
	Policy     decision           `json:"policy"`
	Rollback   rollbackPlan       `json:"rollback"`
	CreatedBy  principal          `json:"created_by"`
	CreatedAt  time.Time          `json:"created_at"`
	ExpiresAt  time.Time          `json:"expires_at"`

	// Hash is what an approval binds to, so an approver can say which plan they
	// approved rather than which id they were shown.
	Hash string `json:"hash"`
}

func wirePlan(p apiv2.Plan) plan {
	return plan{
		ID:            p.ID,
		ProjectID:     p.ProjectID,
		EnvironmentID: p.EnvironmentID,
		ReleaseID:     p.ReleaseID,
		BaseRevision:  p.BaseRevision,
		Strategy:      p.Strategy,
		Operations:    wireOperations(p.Operations),
		Policy: decision{
			Allowed:      p.Policy.Allowed,
			Requirements: mapped(p.Policy.Requirements, wireRequirement),
			Reasons:      mapped(p.Policy.Reasons, wireReason),
			Risk: riskAssessment{
				Level:   p.Policy.Risk.Level,
				Score:   p.Policy.Risk.Score,
				Factors: mapped(p.Policy.Risk.Factors, wireRiskFactor),
			},
		},
		Rollback: rollbackPlan{
			FromRelease: p.Rollback.FromRelease,
			ToRelease:   p.Rollback.ToRelease,
			Operations:  wireOperations(p.Rollback.Operations),
			Automatic:   p.Rollback.Automatic,
		},
		CreatedBy: wirePrincipal(p.CreatedBy),
		CreatedAt: p.CreatedAt,
		ExpiresAt: p.ExpiresAt,
		Hash:      p.Hash,
	}
}

func wireOperations(ops []apiv2.PlannedOperation) []plannedOperation {
	return mapped(ops, func(o apiv2.PlannedOperation) plannedOperation {
		return plannedOperation{
			ID:           o.ID,
			Target:       o.Target,
			Kind:         o.Kind,
			Summary:      o.Summary,
			Changes:      mapped(o.Changes, wireChange),
			Dependencies: o.Dependencies,
			Reversible:   o.Reversible,
		}
	})
}

func wireChange(c apiv2.Change) change {
	return change{Path: c.Path, From: c.From, To: c.To, Sensitive: c.Sensitive}
}

func wireReason(r apiv2.Reason) reason { return reason{Code: r.Code, Message: r.Message} }

func wireRiskFactor(f apiv2.RiskFactor) reason { return reason{Code: f.Code, Message: f.Message} }

func wireRequirement(r apiv2.Requirement) requirement {
	return requirement{Type: r.Type, Role: r.Role, Count: r.Count, Detail: r.Detail}
}

type verifier struct {
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type measurement struct {
	Name  string  `json:"name"`
	Value float64 `json:"value"`
}

type evidenceRef struct {
	Kind string `json:"kind"`
	URI  string `json:"uri"`
}

type check struct {
	Verifier     verifier      `json:"verifier"`
	Verdict      string        `json:"verdict"`
	Measurements []measurement `json:"measurements"`

	// StartedAt and FinishedAt are this check's window, which is not the run's:
	// a run's window spans every check it combined.
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`

	Reason   string        `json:"reason,omitempty"`
	Evidence []evidenceRef `json:"evidence"`
}

type verificationRun struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deployment_id"`
	PlanID       string `json:"plan_id"`

	// Verdict is derived from the checks rather than stored beside them, so a
	// reader cannot be shown a summary that disagrees with what it summarises.
	Verdict string `json:"verdict"`

	Checks     []check   `json:"checks"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Actor      principal `json:"actor"`
}

func wireVerificationRun(r apiv2.VerificationRun) verificationRun {
	return verificationRun{
		ID:           r.ID,
		DeploymentID: r.DeploymentID,
		PlanID:       r.PlanID,
		Verdict:      r.Verdict,
		Checks:       mapped(r.Checks, wireCheck),
		StartedAt:    r.StartedAt,
		FinishedAt:   r.FinishedAt,
		Actor:        wirePrincipal(r.Actor),
	}
}

func wireCheck(c apiv2.Check) check {
	return check{
		Verifier: verifier{
			Kind:    c.Verifier.Kind,
			Name:    c.Verifier.Name,
			Version: c.Verifier.Version,
		},
		Verdict:      c.Verdict,
		Measurements: mapped(c.Measurements, wireMeasurement),
		StartedAt:    c.StartedAt,
		FinishedAt:   c.FinishedAt,
		Reason:       c.Reason,
		Evidence:     mapped(c.Evidence, wireEvidence),
	}
}

func wireMeasurement(m apiv2.Measurement) measurement {
	return measurement{Name: m.Name, Value: m.Value}
}

func wireEvidence(e apiv2.EvidenceRef) evidenceRef {
	return evidenceRef{Kind: e.Kind, URI: e.URI}
}

type event struct {
	ID   string `json:"id"`
	Type string `json:"type"`

	// Version is the schema version of Payload, per type. A reader that does
	// not recognise the pair tolerates it rather than failing (§16.4).
	Version int `json:"version"`

	AggregateType string            `json:"aggregate_type"`
	AggregateID   string            `json:"aggregate_id"`
	Sequence      uint64            `json:"sequence"`
	Time          time.Time         `json:"time"`
	Principal     principal         `json:"principal"`
	CorrelationID string            `json:"correlation_id,omitempty"`
	CausationID   string            `json:"causation_id,omitempty"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

func wireEvent(e apiv2.Event) event {
	return event{
		ID:            e.ID,
		Type:          e.Type,
		Version:       e.Version,
		AggregateType: e.AggregateType,
		AggregateID:   e.AggregateID,
		Sequence:      e.Sequence,
		Time:          e.Time,
		Principal:     wirePrincipal(e.Principal),
		CorrelationID: e.CorrelationID,
		CausationID:   e.CausationID,
		Payload:       e.Payload,
		Metadata:      e.Metadata,
	}
}

// mapped converts a slice, and returns an empty slice rather than nil for an
// empty input: a caller reading `"checks": []` knows there were none, where
// `"checks": null` reads as a field nobody filled in.
func mapped[In, Out any](in []In, f func(In) Out) []Out {
	out := make([]Out, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}
