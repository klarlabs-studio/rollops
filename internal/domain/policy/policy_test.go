package policy_test

import (
	"errors"
	"testing"

	"go.klarlabs.de/rollops/internal/domain/canonical"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

func assessment() policy.RiskAssessment {
	return policy.RiskAssessment{
		Level: policy.RiskMedium,
		Score: 0.48,
		Factors: []policy.RiskFactor{
			{Code: "production_environment", Message: "Deployment targets a production environment"},
		},
	}
}

func TestAWellFormedAssessmentIsValid(t *testing.T) {
	if err := assessment().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestTheScoreMustBeAProportion(t *testing.T) {
	for _, score := range []float64{-0.1, 1.1} {
		r := assessment()
		r.Score = score
		if err := r.Validate(); err == nil {
			t.Errorf("a score of %v was accepted", score)
		}
	}
}

func TestAnUnknownLevelIsRefused(t *testing.T) {
	r := assessment()
	r.Level = "spicy"
	if err := r.Validate(); err == nil {
		t.Error("an unknown risk level was accepted")
	}
}

// Section 12.4 forbids an opaque score from being the sole authorization
// mechanism. A raised level with nothing to point at is exactly that, so the
// explanation is required rather than recommended.
func TestRaisedRiskMustBeExplainable(t *testing.T) {
	for _, level := range []policy.RiskLevel{policy.RiskMedium, policy.RiskHigh, policy.RiskCritical} {
		r := policy.RiskAssessment{Level: level, Score: 0.5}
		if err := r.Validate(); !errors.Is(err, policy.ErrUnexplainedRisk) {
			t.Errorf("%s risk with no factors gave %v, want ErrUnexplainedRisk", level, err)
		}
	}
}

func TestLowRiskNeedsNoExplanation(t *testing.T) {
	r := policy.RiskAssessment{Level: policy.RiskLow, Score: 0}
	if err := r.Validate(); err != nil {
		t.Errorf("low risk with no factors was refused: %v", err)
	}
}

func TestAFactorMustSayWhatItIs(t *testing.T) {
	r := assessment()
	r.Factors = []policy.RiskFactor{{Message: "something happened"}}
	if err := r.Validate(); err == nil {
		t.Error("a factor with no code was accepted")
	}
}

// Levels are compared, not merely labelled: a policy that blocks at or above
// high needs an order to test against.
func TestLevelsAreOrdered(t *testing.T) {
	ascending := []policy.RiskLevel{
		policy.RiskLow, policy.RiskMedium, policy.RiskHigh, policy.RiskCritical,
	}
	for i := 1; i < len(ascending); i++ {
		lower, higher := ascending[i-1], ascending[i]
		if !lower.Below(higher) {
			t.Errorf("%s is not below %s", lower, higher)
		}
		if higher.Below(lower) {
			t.Errorf("%s is below %s", higher, lower)
		}
	}
}

func TestALevelIsNotBelowItself(t *testing.T) {
	if policy.RiskHigh.Below(policy.RiskHigh) {
		t.Error("high is below itself")
	}
}

// An unrecognised level must not sort as harmless. A typo in configuration
// should fail closed.
func TestAnUnknownLevelSortsAsTheMostSevere(t *testing.T) {
	if policy.RiskLevel("spicy").Below(policy.RiskCritical) {
		t.Error("an unknown level sorted below critical")
	}
	if !policy.RiskLow.Below(policy.RiskLevel("spicy")) {
		t.Error("an unknown level did not outrank low")
	}
}

func decision() policy.Decision {
	return policy.Decision{
		Allowed: false,
		Requirements: []policy.Requirement{
			{Type: policy.RequireApproval, Role: "production-approver", Count: 1},
		},
		Reasons: []policy.Reason{{Code: "production_gate", Message: "Production requires approval"}},
		Risk:    assessment(),
	}
}

func TestAWellFormedDecisionIsValid(t *testing.T) {
	if err := decision().Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// A refusal that does not say why is not reviewable, and the operator cannot
// act on it. Section 12.2 lists reasons alongside requirements for this.
func TestARefusalMustGiveAReason(t *testing.T) {
	d := decision()
	d.Reasons = nil
	if err := d.Validate(); !errors.Is(err, policy.ErrUnexplainedDecision) {
		t.Errorf("a refusal with no reasons gave %v, want ErrUnexplainedDecision", err)
	}
}

func TestAnAllowedDecisionNeedsNoReason(t *testing.T) {
	d := decision()
	d.Allowed = true
	d.Requirements = nil
	d.Reasons = nil
	if err := d.Validate(); err != nil {
		t.Errorf("an allowed decision with no reasons was refused: %v", err)
	}
}

func TestAnUnknownRequirementTypeIsRefused(t *testing.T) {
	d := decision()
	d.Requirements = []policy.Requirement{{Type: "vibes"}}
	if err := d.Validate(); err == nil {
		t.Error("an unknown requirement type was accepted")
	}
}

// A requirement for zero approvals is satisfied by doing nothing, which reads
// as a gate but is not one.
func TestAnApprovalRequirementMustDemandAtLeastOne(t *testing.T) {
	d := decision()
	d.Requirements = []policy.Requirement{{Type: policy.RequireApproval, Count: 0}}
	if err := d.Validate(); err == nil {
		t.Error("an approval requirement for zero approvals was accepted")
	}
}

func TestAReasonMustSayWhatItIs(t *testing.T) {
	d := decision()
	d.Reasons = []policy.Reason{{Message: "because"}}
	if err := d.Validate(); err == nil {
		t.Error("a reason with no code was accepted")
	}
}

// "Allowed" is not the same as "may proceed". A decision that allows but still
// demands an approval is the ordinary production case, and reading Allowed
// alone would apply a change that nobody has approved yet.
func TestAllowedWithOutstandingRequirementsIsNotPermission(t *testing.T) {
	d := decision()
	d.Allowed = true
	if err := d.SatisfiedBy(thePlan(), nil, later); !errors.Is(err, policy.ErrRequirementUnmet) {
		t.Errorf("err = %v, want an unapproved decision to withhold permission", err)
	}
}

func TestAnInvalidRiskInvalidatesTheDecision(t *testing.T) {
	d := decision()
	d.Risk.Score = 7
	if err := d.Validate(); err == nil {
		t.Error("a decision carrying an invalid assessment was accepted")
	}
}

// Requirements are what stands between a plan and an apply, so they are part of
// what the plan hash covers. Encoding them must be deterministic and must not
// let one requirement be swapped for another.
func TestDecisionsEncodeCanonically(t *testing.T) {
	var a, b canonical.Writer
	decision().Encode(&a)
	decision().Encode(&b)
	if string(a.Bytes()) != string(b.Bytes()) {
		t.Fatal("the same decision encoded differently")
	}

	relaxed := decision()
	relaxed.Allowed = true

	var c canonical.Writer
	relaxed.Encode(&c)
	if string(a.Bytes()) == string(c.Bytes()) {
		t.Error("flipping Allowed did not change the encoding")
	}
}

func TestChangingARequirementChangesTheEncoding(t *testing.T) {
	var one, two canonical.Writer
	decision().Encode(&one)

	weakened := decision()
	weakened.Requirements[0].Count = 0
	weakened.Encode(&two)

	if string(one.Bytes()) == string(two.Bytes()) {
		t.Error("weakening an approval requirement did not change the encoding")
	}
}

func TestChangingTheRiskChangesTheEncoding(t *testing.T) {
	var one, two canonical.Writer
	decision().Encode(&one)

	riskier := decision()
	riskier.Risk.Score = 0.99
	riskier.Encode(&two)

	if string(one.Bytes()) == string(two.Bytes()) {
		t.Error("changing the risk score did not change the encoding")
	}
}

// Reasons explain; they do not gate. Two decisions that differ only in wording
// still differ as records, so the encoding covers them — a reason removed after
// approval would otherwise leave the hash intact.
func TestChangingAReasonChangesTheEncoding(t *testing.T) {
	var one, two canonical.Writer
	decision().Encode(&one)

	quiet := decision()
	quiet.Reasons = nil
	quiet.Encode(&two)

	if string(one.Bytes()) == string(two.Bytes()) {
		t.Error("dropping the reasons did not change the encoding")
	}
}
