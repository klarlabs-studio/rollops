package policyv1_test

import (
	"errors"
	"testing"

	policyv1 "go.klarlabs.de/rollops/pkg/policy/v1"
)

func approval() policyv1.Requirement {
	return policyv1.Requirement{Kind: policyv1.RequireHumanApproval}
}

// TestAPolicyThatSaidNothingDoesNotAuthorize keeps the permissive answer off
// the default path. A zero decision is what a caller holds when the engine
// errored, when a code path forgot to evaluate, or when a struct was built and
// never filled — none of which is permission.
func TestAPolicyThatSaidNothingDoesNotAuthorize(t *testing.T) {
	if err := policyv1.Permits(policyv1.PolicyDecision{}, nil); err == nil {
		t.Fatal("an unevaluated policy decision authorized the operation")
	}
}

// TestAnUnmetRequirementIsNotPermission is the policy half of verification's
// MUST. Allowed says the rules do not forbid the operation; it does not say the
// conditions attached to it have been met, and a call site that reads only the
// bool cannot tell the difference.
func TestAnUnmetRequirementIsNotPermission(t *testing.T) {
	d := policyv1.PolicyDecision{Allowed: true, Requirements: []policyv1.Requirement{approval()}}
	if err := policyv1.Permits(d, nil); err == nil {
		t.Fatal("a decision requiring human approval authorized without one")
	}
}

// TestAnUnmetRequirementIsNotADenial keeps the two refusals apart. "A human has
// not approved yet" is the gate waiting; "the rules refuse" is the end of the
// road. A caller that cannot tell them apart either fails a rollout that should
// pause, or pauses one that should never run.
func TestAnUnmetRequirementIsNotADenial(t *testing.T) {
	d := policyv1.PolicyDecision{Allowed: true, Requirements: []policyv1.Requirement{approval()}}
	err := policyv1.Permits(d, nil)

	var unmet *policyv1.Unmet
	if !errors.As(err, &unmet) {
		t.Fatalf("err = %v, want an *Unmet naming the requirement", err)
	}
	if unmet.Requirement.Kind != policyv1.RequireHumanApproval {
		t.Errorf("unmet requirement = %q, want %q", unmet.Requirement.Kind, policyv1.RequireHumanApproval)
	}
	var denied *policyv1.Denied
	if errors.As(err, &denied) {
		t.Error("a pending approval was reported as a denial")
	}
}

// TestADenialSaysWhy: a refusal with no reason is an assertion. Whoever is
// stopped has to be told what stopped them.
func TestADenialSaysWhy(t *testing.T) {
	d := policyv1.PolicyDecision{
		Reasons: []policyv1.Reason{{Code: "frozen", Message: "change freeze until 2026-09-30"}},
	}
	err := policyv1.Permits(d, nil)

	var denied *policyv1.Denied
	if !errors.As(err, &denied) {
		t.Fatalf("err = %v, want a *Denied", err)
	}
	if len(denied.Reasons) == 0 {
		t.Fatal("the denial carried no reason")
	}
}

// TestEveryRequirementMustBeMet: satisfying one condition does not satisfy the
// rest. The loop has to reach the last one.
func TestEveryRequirementMustBeMet(t *testing.T) {
	ticket := policyv1.Requirement{Kind: policyv1.RequireChangeTicket}
	d := policyv1.PolicyDecision{
		Allowed:      true,
		Requirements: []policyv1.Requirement{approval(), ticket},
	}
	err := policyv1.Permits(d, func(r policyv1.Requirement) bool {
		return r.Kind == policyv1.RequireHumanApproval
	})

	var unmet *policyv1.Unmet
	if !errors.As(err, &unmet) {
		t.Fatalf("err = %v, want the change-ticket requirement to still block", err)
	}
	if unmet.Requirement.Kind != policyv1.RequireChangeTicket {
		t.Errorf("unmet = %q, want %q", unmet.Requirement.Kind, policyv1.RequireChangeTicket)
	}
}

// TestAnAllowedDecisionWithEveryConditionMetPermits is the one path through
// this function that says yes.
func TestAnAllowedDecisionWithEveryConditionMetPermits(t *testing.T) {
	d := policyv1.PolicyDecision{
		Allowed:      true,
		Requirements: []policyv1.Requirement{approval(), {Kind: policyv1.RequireChangeTicket}},
	}
	if err := policyv1.Permits(d, func(policyv1.Requirement) bool { return true }); err != nil {
		t.Fatalf("a decision with every requirement satisfied was refused: %v", err)
	}
}

// TestRiskDoesNotAuthorizeOnItsOwn is §12.4 as a test. Risk is an input to
// policy, not a parallel authorization system: a critical score cannot refuse
// an operation policy allowed, and a harmless one cannot rescue an operation
// policy denied. An opaque score that could do either would be the sole
// authorization mechanism the spec forbids.
func TestRiskDoesNotAuthorizeOnItsOwn(t *testing.T) {
	critical := policyv1.RiskAssessment{Level: policyv1.RiskCritical, Score: 0.99}
	if err := policyv1.Permits(policyv1.PolicyDecision{Allowed: true, Risk: critical}, nil); err != nil {
		t.Errorf("a critical risk score refused an operation policy allowed: %v", err)
	}

	harmless := policyv1.RiskAssessment{Level: policyv1.RiskLow, Score: 0.01}
	if err := policyv1.Permits(policyv1.PolicyDecision{Risk: harmless}, nil); err == nil {
		t.Error("a low risk score authorized an operation policy did not allow")
	}
}

// TestAMissingSatisfierMeetsNothing: Permits fails closed on a caller that
// passed no way to check its requirements, rather than reading "I cannot tell"
// as "all satisfied".
func TestAMissingSatisfierMeetsNothing(t *testing.T) {
	d := policyv1.PolicyDecision{Allowed: true, Requirements: []policyv1.Requirement{approval()}}
	if err := policyv1.Permits(d, nil); err == nil {
		t.Fatal("a nil satisfier was treated as satisfying every requirement")
	}
}

// TestAnAllowedDecisionWithNoConditionsPermits: the ordinary case must not be
// accidentally blocked by the fail-closed handling around it.
func TestAnAllowedDecisionWithNoConditionsPermits(t *testing.T) {
	if err := policyv1.Permits(policyv1.PolicyDecision{Allowed: true}, nil); err != nil {
		t.Fatalf("an unconditional allow was refused: %v", err)
	}
}
