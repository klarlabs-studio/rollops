package plan_test

import (
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

func TestTheHashCoversThePlan(t *testing.T) {
	p := newPlan(t)
	if err := p.VerifyHash(); err != nil {
		t.Fatalf("a freshly built plan failed its own hash check: %v", err)
	}
}

// Every field that apply relies on must be inside the hash. A plan is stored,
// and storage is the boundary an edit arrives through: if a field can be
// changed without disturbing the hash, it can be changed after approval.
func TestEveryMeaningfulFieldIsHashed(t *testing.T) {
	edits := map[string]func(*plan.DeploymentPlan){
		"release":          func(p *plan.DeploymentPlan) { p.ReleaseID = "rel_other" },
		"environment":      func(p *plan.DeploymentPlan) { p.EnvironmentID = "env_other" },
		"project":          func(p *plan.DeploymentPlan) { p.ProjectID = "prj_other" },
		"base revision":    func(p *plan.DeploymentPlan) { p.BaseRevision = 43 },
		"operation target": func(p *plan.DeploymentPlan) { p.Operations[0].Target = "k8s-staging" },
		"operation kind":   func(p *plan.DeploymentPlan) { p.Operations[0].Kind = plan.OperationRollback },
		"operation diff":   func(p *plan.DeploymentPlan) { p.Operations[0].Diff.Changes[0].To = "app@sha256:evil" },
		"reversibility":    func(p *plan.DeploymentPlan) { p.Operations[0].Reversible = false },
		"policy outcome":   func(p *plan.DeploymentPlan) { p.Policy.Allowed = !p.Policy.Allowed },
		"policy risk":      func(p *plan.DeploymentPlan) { p.Policy.Risk.Score = 0.01 },
		"rollback target":  func(p *plan.DeploymentPlan) { p.Rollback.ToRelease = "rel_other" },
		"expiry":           func(p *plan.DeploymentPlan) { p.ExpiresAt = p.ExpiresAt.Add(time.Hour) },
		"author":           func(p *plan.DeploymentPlan) { p.CreatedBy.ID = "mallory" },
	}
	for name, edit := range edits {
		t.Run(name, func(t *testing.T) {
			p := newPlan(t)
			edit(&p)
			if err := p.VerifyHash(); !errors.Is(err, plan.ErrPlanTampered) {
				t.Errorf("editing the %s gave %v, want ErrPlanTampered", name, err)
			}
		})
	}
}

// Adding a requirement is not the attack; removing one is. Both must move the
// hash, or an approval gate could be deleted from a stored plan.
func TestRemovingAnApprovalRequirementIsDetected(t *testing.T) {
	d := draft()
	d.Policy.Requirements = []policy.Requirement{
		{Type: policy.RequireApproval, Role: "production-approver", Count: 2},
	}
	p := newPlanFrom(t, d)

	p.Policy.Requirements = nil
	if err := p.VerifyHash(); !errors.Is(err, plan.ErrPlanTampered) {
		t.Errorf("removing the approval requirement gave %v, want ErrPlanTampered", err)
	}
}

func TestWeakeningAnApprovalRequirementIsDetected(t *testing.T) {
	d := draft()
	d.Policy.Requirements = []policy.Requirement{
		{Type: policy.RequireApproval, Role: "production-approver", Count: 2},
	}
	p := newPlanFrom(t, d)

	p.Policy.Requirements[0].Count = 1
	if err := p.VerifyHash(); !errors.Is(err, plan.ErrPlanTampered) {
		t.Errorf("weakening the approval requirement gave %v, want ErrPlanTampered", err)
	}
}

// Reordering operations changes what happens and when, so order is identity.
func TestReorderingOperationsIsDetected(t *testing.T) {
	d := draft()
	d.Operations = []plan.PlannedOperation{
		{ID: "op_1", Target: "t", Kind: plan.OperationApply, Summary: "first"},
		{ID: "op_2", Target: "t", Kind: plan.OperationApply, Summary: "second"},
	}
	p := newPlanFrom(t, d)

	p.Operations[0], p.Operations[1] = p.Operations[1], p.Operations[0]
	if err := p.VerifyHash(); !errors.Is(err, plan.ErrPlanTampered) {
		t.Errorf("reordering operations gave %v, want ErrPlanTampered", err)
	}
}

func TestDroppingAnOperationIsDetected(t *testing.T) {
	d := draft()
	d.Operations = []plan.PlannedOperation{
		{ID: "op_1", Target: "t", Kind: plan.OperationApply, Summary: "first"},
		{ID: "op_2", Target: "t", Kind: plan.OperationApply, Summary: "second"},
	}
	p := newPlanFrom(t, d)

	p.Operations = p.Operations[:1]
	if err := p.VerifyHash(); !errors.Is(err, plan.ErrPlanTampered) {
		t.Errorf("dropping an operation gave %v, want ErrPlanTampered", err)
	}
}

// Two plans that differ only in identity must not share a hash, or one could be
// presented in place of the other.
func TestTwoPlansDoNotShareAHash(t *testing.T) {
	a, b := newPlan(t), newPlan(t)
	if a.ID == b.ID {
		t.Fatal("the fixture produced two plans with one id")
	}
	if a.Hash == b.Hash {
		t.Error("two distinct plans share a hash")
	}
}

func TestANotYetExpiredPlanIsApplicable(t *testing.T) {
	p := newPlan(t)
	if err := p.CheckApplicable(at.Add(time.Minute), 42); err != nil {
		t.Errorf("CheckApplicable: %v", err)
	}
}

func TestAnExpiredPlanIsRefused(t *testing.T) {
	p := newPlan(t)
	if err := p.CheckApplicable(at.Add(2*time.Hour), 42); !errors.Is(err, plan.ErrPlanExpired) {
		t.Errorf("got %v, want ErrPlanExpired", err)
	}
}

// Expiry is a deadline, not a window that includes its own end: at the instant
// it expires the plan is already too old to trust.
func TestAPlanExpiresAtItsDeadline(t *testing.T) {
	p := newPlan(t)
	if err := p.CheckApplicable(p.ExpiresAt, 42); !errors.Is(err, plan.ErrPlanExpired) {
		t.Errorf("at the deadline got %v, want ErrPlanExpired", err)
	}
}

// The plan was computed against a revision of the world. If that moved, the
// plan describes a change from a state that no longer exists — so apply must
// refuse rather than silently re-plan (spec 4.9).
func TestAPlanBuiltOnAStaleRevisionIsRefused(t *testing.T) {
	p := newPlan(t)
	err := p.CheckApplicable(at.Add(time.Minute), 43)
	if !errors.Is(err, plan.ErrPlanStale) {
		t.Errorf("got %v, want ErrPlanStale", err)
	}
}

func TestATamperedPlanIsRefusedByCheckApplicable(t *testing.T) {
	p := newPlan(t)
	p.Operations[0].Target = "k8s-somewhere-else"
	if err := p.CheckApplicable(at.Add(time.Minute), 42); !errors.Is(err, plan.ErrPlanTampered) {
		t.Errorf("got %v, want ErrPlanTampered", err)
	}
}

// Order matters: a tampered plan must be reported as tampered even when it is
// also expired, because tampering is the finding that needs investigating and
// expiry is routine.
func TestTamperingIsReportedAheadOfExpiry(t *testing.T) {
	p := newPlan(t)
	p.Operations[0].Target = "k8s-somewhere-else"
	if err := p.CheckApplicable(at.Add(2*time.Hour), 42); !errors.Is(err, plan.ErrPlanTampered) {
		t.Errorf("got %v, want ErrPlanTampered", err)
	}
}

// A plan is not permission. Policy may allow the change while still demanding
// an approval that has not happened, and CheckApplicable must not paper over
// that — the caller decides whether the requirements are met.
func TestAnOutstandingRequirementDoesNotBlockTheStructuralCheck(t *testing.T) {
	d := draft()
	d.Policy.Requirements = []policy.Requirement{
		{Type: policy.RequireApproval, Role: "production-approver", Count: 1},
	}
	p := newPlanFrom(t, d)

	if err := p.CheckApplicable(at.Add(time.Minute), 42); err != nil {
		t.Errorf("CheckApplicable: %v", err)
	}
	if p.Policy.Satisfied() {
		t.Error("the plan reported policy as satisfied with an approval outstanding")
	}
}

// A refused plan is still a plan: it is recorded and explained. What must not
// happen is treating it as applicable.
func TestARefusedPlanIsNotApplicable(t *testing.T) {
	d := draft()
	d.Policy = policy.Decision{
		Allowed: false,
		Reasons: []policy.Reason{{Code: "production_gate", Message: "needs approval"}},
		Risk: policy.RiskAssessment{
			Level:   policy.RiskHigh,
			Score:   0.9,
			Factors: []policy.RiskFactor{{Code: "production_environment", Message: "targets production"}},
		},
	}
	p := newPlanFrom(t, d)

	if p.Policy.Satisfied() {
		t.Error("a refused plan reported policy as satisfied")
	}
	if err := p.CheckApplicable(at.Add(time.Minute), 42); err != nil {
		t.Errorf("the structural check should still pass: %v", err)
	}
}

// Diffs are rendered wherever a plan is explained and stored alongside it, so a
// value marked sensitive must not survive into either (INV-011).
func TestASensitiveChangeIsRedacted(t *testing.T) {
	d := draft()
	d.Operations[0].Diff = plan.Diff{Changes: []plan.Change{
		{Path: "env.DATABASE_URL", From: "postgres://old", To: "postgres://user:hunter2@host", Sensitive: true},
		{Path: "replicas", From: "2", To: "3"},
	}}
	p := newPlanFrom(t, d)

	got := p.Redacted()
	sensitive := got.Operations[0].Diff.Changes[0]
	if sensitive.From != "" || sensitive.To != "" {
		t.Errorf("a sensitive change kept its values: %+v", sensitive)
	}
	if sensitive.Path != "env.DATABASE_URL" {
		t.Errorf("redaction removed the path too: %q", sensitive.Path)
	}

	ordinary := got.Operations[0].Diff.Changes[1]
	if ordinary.To != "3" {
		t.Errorf("an ordinary change was redacted: %+v", ordinary)
	}
}

// Redaction must not silently invalidate the plan it produces: a redacted plan
// is what gets stored, and a stored plan has to verify.
func TestARedactedPlanStillVerifies(t *testing.T) {
	d := draft()
	d.Operations[0].Diff = plan.Diff{Changes: []plan.Change{
		{Path: "env.TOKEN", From: "a", To: "b", Sensitive: true},
	}}
	p := newPlanFrom(t, d)

	got := p.Redacted()
	if err := got.VerifyHash(); err != nil {
		t.Errorf("a redacted plan failed its hash check: %v", err)
	}
}

// The original must be unchanged: Redacted returns a copy, and a caller that
// still holds the plan should not find its diff emptied underneath it.
func TestRedactionDoesNotMutateTheOriginal(t *testing.T) {
	d := draft()
	d.Operations[0].Diff = plan.Diff{Changes: []plan.Change{
		{Path: "env.TOKEN", From: "a", To: "b", Sensitive: true},
	}}
	p := newPlanFrom(t, d)

	_ = p.Redacted()
	if p.Operations[0].Diff.Changes[0].To != "b" {
		t.Error("Redacted emptied the plan it was called on")
	}
}

func newPlanFrom(t *testing.T, d plan.DeploymentPlan) plan.DeploymentPlan {
	t.Helper()
	p, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}
