package plan_test

import (
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// One generator is shared across the package so that two plans built by the
// same test get different identifiers. A generator created per call would
// restart the sequence and hand both of them the same id, which would quietly
// turn "two plans do not share a hash" into a test of nothing.
var gen = identity.NewSequenceGenerator()

func newGen() identity.Generator { return gen }

func newClock() identity.Clock { return identity.NewFixedClock(at) }

func author() identity.Principal {
	return identity.Principal{ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada"}
}

func draft() plan.DeploymentPlan {
	return plan.DeploymentPlan{
		ProjectID:     "prj_1",
		EnvironmentID: "env_1",
		ReleaseID:     "rel_1",
		BaseRevision:  42,
		Strategy:      deployment.StrategyCanary,
		Operations: []plan.PlannedOperation{
			{
				ID:         "op_1",
				Target:     "k8s-production",
				Kind:       plan.OperationApply,
				Summary:    "Update api image digest",
				Reversible: true,
				Diff: plan.Diff{Changes: []plan.Change{
					{Path: "spec.template.spec.containers[0].image", From: "app@sha256:aa", To: "app@sha256:bb"},
				}},
			},
		},
		Policy: policy.Decision{
			Allowed: true,
			Risk: policy.RiskAssessment{
				Level:   policy.RiskMedium,
				Score:   0.48,
				Factors: []policy.RiskFactor{{Code: "production_environment", Message: "targets production"}},
			},
		},
		Rollback: plan.RollbackPlan{
			FromRelease: "rel_0",
			ToRelease:   "rel_1",
			Automatic:   true,
		},
	}
}

func newPlan(t *testing.T) plan.DeploymentPlan {
	t.Helper()
	p, err := plan.New(newGen(), newClock(), author(), time.Hour, draft())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestANewPlanIsStampedAndValid(t *testing.T) {
	p := newPlan(t)
	if p.ID == "" {
		t.Error("no id")
	}
	if !p.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %s, want %s", p.CreatedAt, at)
	}
	if want := at.Add(time.Hour); !p.ExpiresAt.Equal(want) {
		t.Errorf("ExpiresAt = %s, want %s", p.ExpiresAt, want)
	}
	if p.Hash.IsZero() {
		t.Error("no hash")
	}
	if err := p.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// A plan with no expiry never goes stale by time, which defeats the point of
// planning separately from applying.
func TestAPlanMustExpire(t *testing.T) {
	_, err := plan.New(newGen(), newClock(), author(), 0, draft())
	if err == nil {
		t.Error("a plan with no lifetime was accepted")
	}
}

func TestAPlanMustHaveSomethingToDo(t *testing.T) {
	d := draft()
	d.Operations = nil
	_, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
	if err == nil {
		t.Error("a plan with no operations was accepted")
	}
}

func TestTheAuthorIsRecordedAndRedacted(t *testing.T) {
	by := author()
	by.Claims = map[string]string{"token": "s3cret"}
	p, err := plan.New(newGen(), newClock(), by, time.Hour, draft())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.CreatedBy.ID != "u1" {
		t.Errorf("CreatedBy.ID = %q, want %q", p.CreatedBy.ID, "u1")
	}
	// A plan is persisted and rendered wherever a release is explained, so a
	// credential that reached it would be impossible to recall (INV-012).
	if got, ok := p.CreatedBy.Claims["token"]; ok && got == "s3cret" {
		t.Error("the author's credential survived into the plan")
	}
}

func TestOperationIdentifiersMustBeUniqueWithinThePlan(t *testing.T) {
	d := draft()
	d.Operations = append(d.Operations, plan.PlannedOperation{
		ID: "op_1", Target: "k8s-production", Kind: plan.OperationApply, Summary: "again",
	})
	_, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
	if !errors.Is(err, plan.ErrDuplicateOperation) {
		t.Errorf("got %v, want ErrDuplicateOperation", err)
	}
}

// A dependency on an operation the plan does not contain cannot be satisfied,
// so the plan could never be applied in full.
func TestADependencyMustNameAnOperationInThePlan(t *testing.T) {
	d := draft()
	d.Operations[0].Dependencies = []plan.OperationID{"op_missing"}
	_, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
	if !errors.Is(err, plan.ErrUnknownDependency) {
		t.Errorf("got %v, want ErrUnknownDependency", err)
	}
}

func TestAnOperationMayNotDependOnItself(t *testing.T) {
	d := draft()
	d.Operations[0].Dependencies = []plan.OperationID{"op_1"}
	_, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
	if !errors.Is(err, plan.ErrCyclicDependency) {
		t.Errorf("got %v, want ErrCyclicDependency", err)
	}
}

// A cycle has no valid execution order, so it is refused at planning time
// rather than discovered halfway through an apply.
func TestDependencyCyclesAreRefused(t *testing.T) {
	d := draft()
	d.Operations = []plan.PlannedOperation{
		{ID: "op_1", Target: "t", Kind: plan.OperationApply, Summary: "a", Dependencies: []plan.OperationID{"op_3"}},
		{ID: "op_2", Target: "t", Kind: plan.OperationApply, Summary: "b", Dependencies: []plan.OperationID{"op_1"}},
		{ID: "op_3", Target: "t", Kind: plan.OperationApply, Summary: "c", Dependencies: []plan.OperationID{"op_2"}},
	}
	_, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
	if !errors.Is(err, plan.ErrCyclicDependency) {
		t.Errorf("got %v, want ErrCyclicDependency", err)
	}
}

func TestAnAcyclicChainIsAccepted(t *testing.T) {
	d := draft()
	d.Operations = []plan.PlannedOperation{
		{ID: "op_1", Target: "t", Kind: plan.OperationApply, Summary: "a"},
		{ID: "op_2", Target: "t", Kind: plan.OperationApply, Summary: "b", Dependencies: []plan.OperationID{"op_1"}},
		{ID: "op_3", Target: "t", Kind: plan.OperationApply, Summary: "c", Dependencies: []plan.OperationID{"op_1", "op_2"}},
	}
	if _, err := plan.New(newGen(), newClock(), author(), time.Hour, d); err != nil {
		t.Errorf("an acyclic plan was refused: %v", err)
	}
}

func TestAnOperationMustNameATargetAndAKind(t *testing.T) {
	for name, mutate := range map[string]func(*plan.PlannedOperation){
		"no target":  func(o *plan.PlannedOperation) { o.Target = "" },
		"no kind":    func(o *plan.PlannedOperation) { o.Kind = "" },
		"bad kind":   func(o *plan.PlannedOperation) { o.Kind = "target.obliterate" },
		"no id":      func(o *plan.PlannedOperation) { o.ID = "" },
		"no summary": func(o *plan.PlannedOperation) { o.Summary = "" },
	} {
		t.Run(name, func(t *testing.T) {
			d := draft()
			mutate(&d.Operations[0])
			_, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
			if err == nil {
				t.Errorf("an operation with %s was accepted", name)
			}
		})
	}
}

// Section 8.3 asks that a plan be understandable without parsing an opaque
// provider blob. A summary is what makes that true, so it is required.
func TestThePlanCarriesAPortableSummary(t *testing.T) {
	p := newPlan(t)
	if p.Operations[0].Summary == "" {
		t.Error("the operation lost its summary")
	}
}

func TestAnInvalidPolicyDecisionInvalidatesThePlan(t *testing.T) {
	d := draft()
	d.Policy.Risk.Score = 7
	_, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
	if err == nil {
		t.Error("a plan carrying an invalid policy decision was accepted")
	}
}

// The rollback target is the whole point of the record: a rollback to the
// release being deployed is not a rollback.
func TestARollbackMustLeadSomewhereElse(t *testing.T) {
	d := draft()
	d.Rollback = plan.RollbackPlan{FromRelease: "rel_1", ToRelease: "rel_1"}
	_, err := plan.New(newGen(), newClock(), author(), time.Hour, d)
	if err == nil {
		t.Error("a rollback onto the same release was accepted")
	}
}

// A plan with no strategy would be applied under whatever the caller passed,
// which is the drift the hash exists to prevent.
func TestAPlanMustSayHowItRollsOut(t *testing.T) {
	for _, s := range []deployment.Strategy{"", "yolo"} {
		p := newPlan(t)
		p.Strategy = s
		if err := p.Validate(); err == nil {
			t.Errorf("a plan with strategy %q was accepted", s)
		}
	}
}

// A plan that does not say which release, environment or project it concerns
// cannot be applied against anything, and Validate is the only thing standing
// between a partially filled struct and an apply.
func TestAPlanMustNameWhatItActsOn(t *testing.T) {
	for name, blank := range map[string]func(*plan.DeploymentPlan){
		"id":          func(p *plan.DeploymentPlan) { p.ID = "" },
		"project":     func(p *plan.DeploymentPlan) { p.ProjectID = "" },
		"environment": func(p *plan.DeploymentPlan) { p.EnvironmentID = "" },
		"release":     func(p *plan.DeploymentPlan) { p.ReleaseID = "" },
	} {
		t.Run(name, func(t *testing.T) {
			p := newPlan(t)
			blank(&p)
			if err := p.Validate(); err == nil {
				t.Errorf("a plan with no %s was accepted", name)
			}
		})
	}
}

// New always stamps these, but a plan read back from storage is a struct
// someone else filled in. Validate is what stands between that struct and an
// apply, so the stamps are checked rather than assumed.
func TestAReconstitutedPlanMustCarryItsStamps(t *testing.T) {
	for name, break_ := range map[string]func(*plan.DeploymentPlan){
		"no author":        func(p *plan.DeploymentPlan) { p.CreatedBy = identity.Principal{} },
		"no creation time": func(p *plan.DeploymentPlan) { p.CreatedAt = time.Time{} },
		"expiry before creation": func(p *plan.DeploymentPlan) {
			p.ExpiresAt = p.CreatedAt.Add(-time.Minute)
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPlan(t)
			break_(&p)
			if err := p.Validate(); err == nil {
				t.Errorf("a plan with %s was accepted", name)
			}
		})
	}
}

// An operation that changes nothing still has to survive redaction intact: the
// copy is what gets stored, and dropping the operation would lose a step.
func TestRedactingAnOperationWithNoDiffKeepsIt(t *testing.T) {
	d := draft()
	d.Operations[0].Diff = plan.Diff{}
	p := newPlanFrom(t, d)

	got := p.Redacted()
	if len(got.Operations) != 1 {
		t.Fatalf("got %d operations, want 1", len(got.Operations))
	}
	if err := got.VerifyHash(); err != nil {
		t.Errorf("the redacted plan failed its hash check: %v", err)
	}
}

// The rollback is what runs when the deployment fails, so it is held to the
// same standard as the plan it undoes: a cycle discovered then would be
// discovered at the worst possible moment.
func TestTheRollbackOperationsAreValidatedToo(t *testing.T) {
	d := draft()
	d.Rollback.Operations = []plan.PlannedOperation{
		{ID: "op_1", Target: "t", Kind: plan.OperationApply, Summary: "a", Dependencies: []plan.OperationID{"op_1"}},
	}
	if _, err := plan.New(newGen(), newClock(), author(), time.Hour, d); !errors.Is(err, plan.ErrCyclicDependency) {
		t.Errorf("got %v, want ErrCyclicDependency", err)
	}
}

// A plan that never completed New carries no hash. Treating that as "nothing to
// check" would let an unhashed plan apply unverified, so it is refused as
// tampering rather than passed over.
func TestAPlanWithNoHashIsRefused(t *testing.T) {
	p := newPlan(t)
	p.Hash = digest.Digest{}
	if err := p.VerifyHash(); !errors.Is(err, plan.ErrPlanTampered) {
		t.Errorf("got %v, want ErrPlanTampered", err)
	}
}

func TestAPlanMustBeBuiltWithAWorkingGenerator(t *testing.T) {
	failing := identity.GeneratorFunc(func() (string, error) {
		return "", errors.New("no entropy")
	})
	if _, err := plan.New(failing, newClock(), author(), time.Hour, draft()); err == nil {
		t.Error("a plan was built without an identifier")
	}
}

// Redaction walks the rollback as well: a secret is no less exposed for being
// in the operation that undoes the change.
func TestRedactionReachesTheRollback(t *testing.T) {
	d := draft()
	d.Rollback.Operations = []plan.PlannedOperation{{
		ID: "op_r1", Target: "t", Kind: plan.OperationRollback, Summary: "undo",
		Diff: plan.Diff{Changes: []plan.Change{{Path: "env.TOKEN", From: "b", To: "a", Sensitive: true}}},
	}}
	p := newPlanFrom(t, d)

	got := p.Redacted()
	if c := got.Rollback.Operations[0].Diff.Changes[0]; c.From != "" || c.To != "" {
		t.Errorf("a sensitive rollback change kept its values: %+v", c)
	}
	if err := got.VerifyHash(); err != nil {
		t.Errorf("the redacted plan failed its hash check: %v", err)
	}
}

func TestAPlanNeedNotDeclareARollback(t *testing.T) {
	d := draft()
	d.Rollback = plan.RollbackPlan{}
	if _, err := plan.New(newGen(), newClock(), author(), time.Hour, d); err != nil {
		t.Errorf("a plan with no rollback was refused: %v", err)
	}
}
