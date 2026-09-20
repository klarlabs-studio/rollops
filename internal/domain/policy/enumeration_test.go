package policy_test

import (
	"testing"

	"go.klarlabs.de/rollops/internal/domain/policy"
)

func TestTheRiskLevelsRunFromLeastToMostSevere(t *testing.T) {
	t.Parallel()

	got := policy.RiskLevels()
	if len(got) == 0 {
		t.Fatal("RiskLevels() is empty")
	}
	// Below is the package's own ordering. Asserting the enumeration agrees
	// with it means a level inserted in the wrong place fails here rather than
	// quietly reordering whatever renders the list.
	for i := 1; i < len(got); i++ {
		if !got[i-1].Below(got[i]) {
			t.Errorf("RiskLevels() has %q before %q, but %[1]q is not below %[2]q", got[i-1], got[i])
		}
	}
}

func TestALevelOutsideTheEnumerationIsMoreSevereThanAllOfThem(t *testing.T) {
	t.Parallel()

	// A typo in configuration must fail closed, so an unrecognised level
	// outranks every declared one rather than reading as harmless.
	unknown := policy.RiskLevel("mild")
	for _, l := range policy.RiskLevels() {
		if !l.Below(unknown) {
			t.Errorf("declared level %q is not below an unrecognised one", l)
		}
	}
}

func TestEveryRequirementTypeInTheEnumerationStatesAConditionThatCanBeMet(t *testing.T) {
	t.Parallel()

	got := policy.RequirementTypes()
	if len(got) == 0 {
		t.Fatal("RequirementTypes() is empty")
	}
	for _, ty := range got {
		r := policy.Requirement{Type: ty, Detail: "because"}
		if ty == policy.RequireApproval {
			// An approval requirement for zero approvals is refused for its
			// own reasons, which is not what this test is about.
			r.Role, r.Count = "release-manager", 1
		}
		if err := r.Validate(); err != nil {
			t.Errorf("RequirementTypes() lists %q, which Validate() rejects: %v", ty, err)
		}
	}
}

func TestARequirementTypeOutsideTheEnumerationIsRejected(t *testing.T) {
	t.Parallel()

	err := policy.Requirement{Type: policy.RequirementType("vibes")}.Validate()
	if err == nil {
		t.Error("Validate() accepted a requirement type the enumeration does not list")
	}
}
