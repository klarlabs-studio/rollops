package policy_test

import (
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

var (
	planned = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	later   = planned.Add(time.Hour)
)

// thePlan is the revision under approval. Every test that varies it is varying
// the one thing §13 says an approval is bound to.
func thePlan() policy.SubjectRef {
	return policy.SubjectRef{Kind: "plan", ID: "pln_1", Revision: "sha256:aaa"}
}

func granted(by string, subject policy.SubjectRef) policy.Approval {
	return policy.Approval{
		ID:        identity.ApprovalID("apr_" + by),
		Subject:   subject,
		Principal: identity.Principal{ID: by, Type: identity.PrincipalHuman},
		Decision:  policy.ApprovalGranted,
		CreatedAt: planned,
	}
}

func needs(n int) policy.Decision {
	return policy.Decision{
		Allowed:      true,
		Risk:         policy.RiskAssessment{Level: policy.RiskLow},
		Requirements: []policy.Requirement{{Type: policy.RequireApproval, Count: n}},
	}
}

// TestAnApprovalOfAnotherPlanDoesNotCount is §13's MUST. An approval binds to
// the exact revision it approved; if the plan moved, what the approver read is
// not what would be applied, and carrying their approval forward would apply a
// change nobody agreed to.
func TestAnApprovalOfAnotherPlanDoesNotCount(t *testing.T) {
	stale := thePlan()
	stale.Revision = "sha256:bbb"

	err := needs(1).SatisfiedBy(thePlan(), []policy.Approval{granted("ana", stale)}, later)
	if !errors.Is(err, policy.ErrRequirementUnmet) {
		t.Fatalf("err = %v, want the requirement to still stand", err)
	}
}

// TestAnApprovalOfTheSamePlanCounts is the one path that says yes.
func TestAnApprovalOfTheSamePlanCounts(t *testing.T) {
	if err := needs(1).SatisfiedBy(thePlan(), []policy.Approval{granted("ana", thePlan())}, later); err != nil {
		t.Fatalf("an approval of this exact plan was refused: %v", err)
	}
}

// TestAnApprovalOfAnotherSubjectDoesNotCount: matching revisions across
// different subjects would let an approval of one plan discharge another's.
func TestAnApprovalOfAnotherSubjectDoesNotCount(t *testing.T) {
	other := thePlan()
	other.ID = "pln_2"

	err := needs(1).SatisfiedBy(thePlan(), []policy.Approval{granted("ana", other)}, later)
	if !errors.Is(err, policy.ErrRequirementUnmet) {
		t.Fatalf("err = %v, want an approval of another plan to be ignored", err)
	}
}

// TestOnePersonCannotApproveTwice: two approvals are two people. Counting
// repeat clicks would turn a four-eyes requirement into a two-click one.
func TestOnePersonCannotApproveTwice(t *testing.T) {
	a := granted("ana", thePlan())
	again := a
	again.ID = "apr_ana2"

	err := needs(2).SatisfiedBy(thePlan(), []policy.Approval{a, again}, later)
	if !errors.Is(err, policy.ErrRequirementUnmet) {
		t.Fatalf("err = %v, want one principal to count once", err)
	}
	if err := needs(2).SatisfiedBy(thePlan(), []policy.Approval{a, granted("ben", thePlan())}, later); err != nil {
		t.Fatalf("two distinct approvers did not satisfy a count of two: %v", err)
	}
}

// TestARejectionIsDecisive: a principal who refused is not a principal who
// approved, and their refusal is not discharged by someone else approving.
func TestARejectionIsDecisive(t *testing.T) {
	no := granted("ben", thePlan())
	no.Decision = policy.ApprovalDenied
	no.Reason = "waiting on the migration rehearsal"

	err := needs(1).SatisfiedBy(thePlan(), []policy.Approval{granted("ana", thePlan()), no}, later)
	if !errors.Is(err, policy.ErrApprovalDenied) {
		t.Fatalf("err = %v, want a rejection of this plan to block it", err)
	}
}

// TestAnExpiredApprovalDoesNotCount: an approval with an expiry says the
// approver vouched for a window, not forever.
func TestAnExpiredApprovalDoesNotCount(t *testing.T) {
	a := granted("ana", thePlan())
	a.ExpiresAt = planned.Add(time.Minute)

	if err := needs(1).SatisfiedBy(thePlan(), []policy.Approval{a}, planned); err != nil {
		t.Fatalf("an unexpired approval was refused: %v", err)
	}
	err := needs(1).SatisfiedBy(thePlan(), []policy.Approval{a}, later)
	if !errors.Is(err, policy.ErrRequirementUnmet) {
		t.Fatalf("err = %v, want an expired approval to stop counting", err)
	}
}

// TestARoleRequirementNeedsThatRole: "approved by a release manager" is not
// satisfied by anyone who happens to be available.
func TestARoleRequirementNeedsThatRole(t *testing.T) {
	d := policy.Decision{
		Allowed:      true,
		Risk:         policy.RiskAssessment{Level: policy.RiskLow},
		Requirements: []policy.Requirement{{Type: policy.RequireApproval, Count: 1, Role: "release-manager"}},
	}

	if err := d.SatisfiedBy(thePlan(), []policy.Approval{granted("ana", thePlan())}, later); !errors.Is(err, policy.ErrRequirementUnmet) {
		t.Fatalf("err = %v, want a role requirement to stand", err)
	}

	ana := granted("ana", thePlan())
	ana.Principal.Claims = map[string]string{"role": "release-manager"}
	if err := d.SatisfiedBy(thePlan(), []policy.Approval{ana}, later); err != nil {
		t.Fatalf("an approver holding the required role was refused: %v", err)
	}
}

// TestApprovalsDoNotSatisfyOtherRequirements: a human saying yes is not a
// signed artifact. Discharging the wrong condition is how a gate becomes
// decoration.
func TestApprovalsDoNotSatisfyOtherRequirements(t *testing.T) {
	d := policy.Decision{
		Allowed: true,
		Risk:    policy.RiskAssessment{Level: policy.RiskLow},
		Requirements: []policy.Requirement{
			{Type: policy.RequireApproval, Count: 1},
			{Type: policy.RequireSignedArtifact},
		},
	}
	err := d.SatisfiedBy(thePlan(), []policy.Approval{granted("ana", thePlan())}, later)
	if !errors.Is(err, policy.ErrRequirementUnmet) {
		t.Fatalf("err = %v, want the signed-artifact requirement to still stand", err)
	}
}

// TestADeniedDecisionIsNotApprovable: approvals discharge conditions attached
// to an allowed operation. They are not a way to overturn a refusal — that
// would make every denial advisory.
func TestADeniedDecisionIsNotApprovable(t *testing.T) {
	d := policy.Decision{
		Risk:    policy.RiskAssessment{Level: policy.RiskLow},
		Reasons: []policy.Reason{{Code: "frozen", Message: "change freeze"}},
	}
	err := d.SatisfiedBy(thePlan(), []policy.Approval{granted("ana", thePlan())}, later)
	if !errors.Is(err, policy.ErrDecisionRefuses) {
		t.Fatalf("err = %v, want approvals not to overturn a denial", err)
	}
}

// TestAnUnboundApprovalCountsForNothing: the zero value is not permission. An
// approval that names no revision cannot be shown to be about this plan.
func TestAnUnboundApprovalCountsForNothing(t *testing.T) {
	err := needs(1).SatisfiedBy(thePlan(), []policy.Approval{{Decision: policy.ApprovalGranted}}, later)
	if !errors.Is(err, policy.ErrRequirementUnmet) {
		t.Fatalf("err = %v, want an unbound approval to satisfy nothing", err)
	}
}

// TestNoRequirementsNeedNoApprovals keeps the ordinary case from being blocked
// by the fail-closed handling around it.
func TestNoRequirementsNeedNoApprovals(t *testing.T) {
	d := policy.Decision{Allowed: true, Risk: policy.RiskAssessment{Level: policy.RiskLow}}
	if err := d.SatisfiedBy(thePlan(), nil, later); err != nil {
		t.Fatalf("an unconditional allow was refused: %v", err)
	}
}

// TestUnmetNamesTheRequirement: an operator told only "not satisfied" has to
// guess which condition is holding the deployment.
func TestUnmetNamesTheRequirement(t *testing.T) {
	err := needs(2).SatisfiedBy(thePlan(), []policy.Approval{granted("ana", thePlan())}, later)

	var unmet *policy.UnmetError
	if !errors.As(err, &unmet) {
		t.Fatalf("err = %v, want an *UnmetError", err)
	}
	if unmet.Requirement.Type != policy.RequireApproval {
		t.Errorf("unmet requirement = %q", unmet.Requirement.Type)
	}
	if unmet.Have != 1 || unmet.Want != 2 {
		t.Errorf("have/want = %d/%d, want 1/2", unmet.Have, unmet.Want)
	}
}

// TestApprovalValidate: an approval that cannot say what it approved, who
// approved it, or what they decided is not a record anyone can audit.
func TestApprovalValidate(t *testing.T) {
	ok := granted("ana", thePlan())
	if err := ok.Validate(); err != nil {
		t.Fatalf("a complete approval was rejected: %v", err)
	}

	for name, mutate := range map[string]func(*policy.Approval){
		"no subject revision": func(a *policy.Approval) { a.Subject.Revision = "" },
		"no subject id":       func(a *policy.Approval) { a.Subject.ID = "" },
		"no subject kind":     func(a *policy.Approval) { a.Subject.Kind = "" },
		"no principal":        func(a *policy.Approval) { a.Principal = identity.Principal{} },
		"no decision":         func(a *policy.Approval) { a.Decision = "" },
		"no timestamp":        func(a *policy.Approval) { a.CreatedAt = time.Time{} },
	} {
		a := granted("ana", thePlan())
		mutate(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("%s: an incomplete approval validated", name)
		}
	}
}

// TestADenialMustSayWhy: a refusal with no reason leaves whoever was stopped
// with nothing to act on — the same rule the decision itself obeys.
func TestADenialMustSayWhy(t *testing.T) {
	a := granted("ana", thePlan())
	a.Decision = policy.ApprovalDenied
	if err := a.Validate(); err == nil {
		t.Fatal("a rejection with no reason validated")
	}
}
