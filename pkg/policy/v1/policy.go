// Package policyv1 is the authorization contract (§12). One engine answers one
// question — may this principal do this thing to this subject, and on what
// conditions — for every decision point in the system, so authorization is a
// single seam rather than a guardrail here, a risk threshold there and an RBAC
// check somewhere else.
//
// The rule this package exists to hold: risk is an INPUT to policy, never a
// parallel authorization system (§12.4). A RiskAssessment describes a change;
// it has no field that can allow or refuse one. Only a PolicyDecision
// authorizes, and only through Permits.
package policyv1

import (
	"context"
	"fmt"
	"strings"
)

// Action names a decision point (§12.1). Policy must be invokable at every one
// of these, which is why they are named here rather than spelled as literals at
// each call site.
type Action string

const (
	ActionProjectModify      Action = "project.modify"
	ActionEnvironmentModify  Action = "environment.modify"
	ActionPipelineRun        Action = "pipeline.run"
	ActionArtifactRegister   Action = "artifact.register"
	ActionReleaseCreate      Action = "release.create"
	ActionDeploymentPlan     Action = "deployment.plan"
	ActionDeploymentApply    Action = "deployment.apply"
	ActionDeploymentPromote  Action = "deployment.promote"
	ActionDeploymentRollback Action = "deployment.rollback"
	ActionSecretAccess       Action = "secret.access"
	ActionTargetModify       Action = "target.modify"
)

// PrincipalType is who is acting (§4.10).
type PrincipalType string

const (
	PrincipalHuman     PrincipalType = "human"
	PrincipalService   PrincipalType = "service"
	PrincipalAgent     PrincipalType = "agent"
	PrincipalGit       PrincipalType = "git"
	PrincipalScheduler PrincipalType = "scheduler"
	PrincipalSystem    PrincipalType = "system"
)

// Principal is the attributable actor behind an operation. It carries no
// authorization of its own: §4.10 is explicit that the decision does not live
// in the identity, so that changing what someone may do is a policy change
// rather than an identity change.
type Principal struct {
	ID          string
	Type        PrincipalType
	DisplayName string
	Claims      map[string]string
}

// SubjectRef names what is being acted on. Revision is what makes a decision —
// and an approval derived from it — bindable to one exact plan rather than to
// whatever the subject holds later (§13).
type SubjectRef struct {
	Kind     string // "deployment", "release", "environment", "secret", "target"
	ID       string
	Revision string
}

// PolicyEngine answers one authorization question (§12.2).
type PolicyEngine interface {
	Evaluate(context.Context, PolicyInput) (PolicyDecision, error)
}

// PolicyInput is the question. Risk is supplied rather than computed here
// because scoring a change and authorizing it are different jobs: the score is
// evidence the engine weighs, in the same way a verification verdict is.
type PolicyInput struct {
	Action    Action
	Principal Principal
	Subject   SubjectRef
	Risk      RiskAssessment
}

// PolicyDecision is the answer (§12.2).
//
// The zero value denies, which is the whole reason Allowed is a bool that
// starts false: a decision nobody reached is not permission.
type PolicyDecision struct {
	// Allowed reports that the rules do not forbid the operation. It is not
	// permission on its own — see Requirements, and use Permits.
	Allowed      bool
	Requirements []Requirement
	Reasons      []Reason
	Risk         RiskAssessment
}

// RequirementKind is a condition attached to an allowed operation (§12.2).
type RequirementKind string

const (
	RequireHumanApproval  RequirementKind = "human_approval"
	RequireApprovalCount  RequirementKind = "approval_count"
	RequireApproverRole   RequirementKind = "approver_role"
	RequireSignedArtifact RequirementKind = "signed_artifact"
	RequireProvenance     RequirementKind = "provenance"
	RequireStagingSuccess RequirementKind = "staging_deployment"
	RequireTimeWindow     RequirementKind = "time_window"
	RequireChangeTicket   RequirementKind = "change_ticket"
)

// Requirement is one condition that must hold before an allowed operation may
// proceed.
type Requirement struct {
	Kind   RequirementKind
	Count  int    // approvals needed, for RequireApprovalCount
	Role   string // approver role, for RequireApproverRole
	Detail string
}

func (r Requirement) String() string {
	var b strings.Builder
	b.WriteString(string(r.Kind))
	if r.Count > 0 {
		fmt.Fprintf(&b, " (%d)", r.Count)
	}
	if r.Role != "" {
		fmt.Fprintf(&b, " by %s", r.Role)
	}
	if r.Detail != "" {
		b.WriteString(": " + r.Detail)
	}
	return b.String()
}

// Reason explains a decision. A refusal with no reason is an assertion:
// whoever was stopped has to be told what stopped them.
type Reason struct {
	Code    string
	Message string
}

// RiskLevel is the coarse band a score falls in, for humans and for CEL.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// RiskFactor is one explainable contribution to a score. §12.4 asks for these
// because a number nobody can decompose is a number nobody can argue with, and
// an authorization decision has to be arguable.
type RiskFactor struct {
	Name   string
	Weight float64
	Detail string
}

// RiskAssessment is how dangerous a change looks (§12.4).
//
// Note what is absent: there is no Allowed, no NeedsApproval, no Blocked. Risk
// describes; policy decides. The separation is structural rather than a
// convention, so a future scorer cannot quietly become the authorization
// mechanism the spec forbids.
type RiskAssessment struct {
	Level   RiskLevel
	Score   float64
	Factors []RiskFactor
}

// Denied is returned by Permits when policy refuses outright.
type Denied struct {
	Reasons []Reason
}

func (d *Denied) Error() string {
	if len(d.Reasons) == 0 {
		return "policy: denied"
	}
	msgs := make([]string, 0, len(d.Reasons))
	for _, r := range d.Reasons {
		msgs = append(msgs, r.Message)
	}
	return "policy: denied: " + strings.Join(msgs, "; ")
}

// Unmet is returned by Permits when policy allowed the operation but a
// condition it attached has not been satisfied yet.
//
// This is deliberately not a Denied. "No human has approved yet" is the gate
// waiting; "the rules refuse" is the end of the road. A caller that cannot tell
// them apart will either fail a rollout that should pause or pause one that
// should never run.
type Unmet struct {
	Requirement Requirement
}

func (u *Unmet) Error() string {
	return "policy: requirement not satisfied: " + u.Requirement.String()
}

// Permits reports why a decision does not authorize the operation, or nil if it
// does. met answers whether one requirement has actually been satisfied.
//
// This function is the whole reason PolicyDecision is not read field by field
// at call sites. Allowed and Requirements have to be consulted together: a
// caller that branches on Allowed alone grants an operation whose conditions
// nobody checked, which is the authorization version of promoting on a
// verification that never ran.
//
// A nil met satisfies nothing. "I have no way to check" is not "everything
// checks out".
func Permits(d PolicyDecision, met func(Requirement) bool) error {
	if !d.Allowed {
		return &Denied{Reasons: d.Reasons}
	}
	if met == nil {
		met = func(Requirement) bool { return false }
	}
	for _, r := range d.Requirements {
		if !met(r) {
			return &Unmet{Requirement: r}
		}
	}
	return nil
}
