package apiv2_test

import (
	"slices"
	"testing"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/domain/deployment"
)

// What a caller may do next is derived from the same transition table the
// engine enforces, so the list cannot promise an action the next call refuses.
// The table is every status the machine knows, because a status nobody listed
// here would silently offer nothing to do.
func TestWhatMayBeDoneNextFollowsTheStatus(t *testing.T) {
	t.Parallel()

	want := map[deployment.Status][]string{
		deployment.StatusPlanned:          {"cancel"},
		deployment.StatusAwaitingApproval: {"approve", "cancel"},
		deployment.StatusQueued:           {"cancel"},
		deployment.StatusApplying:         {"verify", "rollback", "cancel"},
		deployment.StatusVerifying:        {"promote", "rollback"},
		deployment.StatusPaused:           {"promote", "rollback", "cancel"},
		deployment.StatusPromoting:        {"rollback"},
		deployment.StatusSucceeded:        nil,
		deployment.StatusFailed:           nil,
		deployment.StatusRollingBack:      nil,
		deployment.StatusRolledBack:       nil,
		deployment.StatusCancelled:        nil,
	}

	if got := len(deployment.Statuses()); got != len(want) {
		t.Fatalf("the table covers %d statuses, the machine knows %d", len(want), got)
	}

	for _, s := range deployment.Statuses() {
		t.Run(string(s), func(t *testing.T) {
			t.Parallel()
			got := apiv2.Deployment{Status: string(s)}.NextActions()
			if !slices.Equal(got, want[s]) {
				t.Errorf("next actions = %v, want %v", got, want[s])
			}
		})
	}
}

// A status nothing recognises offers nothing to do rather than everything.
func TestAnUnrecognisedStatusOffersNothing(t *testing.T) {
	t.Parallel()
	if got := (apiv2.Deployment{Status: "sideways"}).NextActions(); got != nil {
		t.Errorf("next actions = %v, want none", got)
	}
}

// A plan policy has already decided what is missing, so the action it offers is
// the one that would supply it — asking an agent to apply a plan policy refuses
// would have it press the button and read the same refusal back.
func TestAPlanOffersApprovalWhenPolicyWantsOneAndApplyWhenItDoesNot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		plan apiv2.Plan
		want []string
	}{
		{
			name: "allowed",
			plan: apiv2.Plan{Policy: apiv2.Decision{Allowed: true}},
			want: []string{"apply_plan"},
		},
		{
			name: "needs an approval",
			plan: apiv2.Plan{Policy: apiv2.Decision{
				Requirements: []apiv2.Requirement{{Type: "approval", Role: "release-manager"}},
			}},
			want: []string{"request_approval"},
		},
		{
			name: "refused outright",
			plan: apiv2.Plan{Policy: apiv2.Decision{
				Reasons: []apiv2.Reason{{Code: "frozen", Message: "change freeze"}},
			}},
			want: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.plan.NextActions(); !slices.Equal(got, tc.want) {
				t.Errorf("next actions = %v, want %v", got, tc.want)
			}
		})
	}
}
