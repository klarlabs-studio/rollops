package apiv2_test

import (
	"context"
	"testing"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/release"
)

var planner = identity.Principal{
	ID:          "ci@example.com",
	Type:        identity.PrincipalService,
	DisplayName: "CI",
	Claims:      map[string]string{"token": "s3cret"},
}

// scene is a project with one environment and one release in it — everything a
// plan needs to exist.
type scene struct {
	*world
	environment environment.Environment
	release     release.Release
}

func (w *world) scene(t *testing.T) scene {
	t.Helper()
	p := w.project(t, "checkout")
	env := w.environment(t, p.ID, environment.Environment{Name: "production"})
	rel := w.release(t, p.ID, "2.0.0", map[string]identity.ArtifactID{"app": w.artifact(t, p.ID, "app").ID})
	return scene{world: w, environment: env, release: rel}
}

func (s scene) plan(t *testing.T, ops []plan.PlannedOperation, decision policy.Decision) plan.DeploymentPlan {
	t.Helper()
	p, err := plan.New(s.ids, s.clock, planner, time.Hour, plan.DeploymentPlan{
		ProjectID:     s.environment.ProjectID,
		EnvironmentID: s.environment.ID,
		ReleaseID:     s.release.ID,
		BaseRevision:  s.environment.Revision,
		Strategy:      deployment.StrategyRolling,
		Operations:    ops,
		Policy:        decision,
	})
	if err != nil {
		t.Fatalf("plan.New: %v", err)
	}
	if err := s.store.Plans().Create(context.Background(), p); err != nil {
		t.Fatalf("Plans.Create: %v", err)
	}
	return p
}

func (s scene) deployment(t *testing.T, planID identity.PlanID) deployment.Deployment {
	t.Helper()
	d, err := deployment.New(s.ids, s.clock, planner, deployment.Deployment{
		ProjectID:     s.environment.ProjectID,
		EnvironmentID: s.environment.ID,
		ReleaseID:     s.release.ID,
		PlanID:        planID,
		Strategy:      deployment.StrategyRolling,
		Trigger:       deployment.Trigger{Type: deployment.TriggerAPI, Detail: "POST /v2/deployment-plans/:apply"},
	})
	if err != nil {
		t.Fatalf("deployment.New: %v", err)
	}
	rev, err := s.store.Deployments().Create(context.Background(), d)
	if err != nil {
		t.Fatalf("Deployments.Create: %v", err)
	}
	d.Revision = rev
	return d
}

func allowed() policy.Decision {
	return policy.Decision{
		Allowed: true,
		Reasons: []policy.Reason{{Code: "no_gate", Message: "no policy is bound to this environment"}},
		Risk:    policy.RiskAssessment{Level: policy.RiskLow, Score: 0.1},
	}
}

func oneApply(changes ...plan.Change) []plan.PlannedOperation {
	return []plan.PlannedOperation{{
		ID:         "op-1",
		Target:     "api",
		Kind:       plan.OperationApply,
		Summary:    "roll the api deployment to 2.0.0",
		Diff:       plan.Diff{Changes: changes},
		Reversible: true,
	}}
}

func TestADeploymentSaysWhatItIsDeployingAndWhereItGotTo(t *testing.T) {
	s := setup(t).scene(t)
	p := s.plan(t, oneApply(), allowed())
	stored := s.deployment(t, p.ID)

	got, err := s.svc.GetDeployment(context.Background(), apiv2.GetDeploymentRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.ReleaseID != string(s.release.ID) || got.EnvironmentID != string(s.environment.ID) {
		t.Errorf("deployment = release %q into %q", got.ReleaseID, got.EnvironmentID)
	}
	if got.PlanID != string(p.ID) {
		t.Errorf("plan id = %q, want %q", got.PlanID, p.ID)
	}
	if got.Status != string(deployment.StatusPlanned) {
		t.Errorf("status = %q, want %q", got.Status, deployment.StatusPlanned)
	}
	if got.Strategy != string(deployment.StrategyRolling) {
		t.Errorf("strategy = %q", got.Strategy)
	}
	if got.Trigger.Type != string(deployment.TriggerAPI) || got.Trigger.Detail == "" {
		t.Errorf("trigger = %+v", got.Trigger)
	}
	if got.Revision == 0 {
		t.Error("revision is zero; a cancel built on this read could not be checked for staleness")
	}
}

func TestADeploymentThatHasNotStartedSaysSoRatherThanNamingTheEpoch(t *testing.T) {
	s := setup(t).scene(t)
	stored := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)

	got, err := s.svc.GetDeployment(context.Background(), apiv2.GetDeploymentRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.StartedAt != nil {
		t.Errorf("started at = %s, want absent", got.StartedAt)
	}
	if got.FinishedAt != nil {
		t.Errorf("finished at = %s, want absent", got.FinishedAt)
	}
	if got.Previous != "" {
		t.Errorf("previous = %q, want empty for a first deployment", got.Previous)
	}
}

func TestTheActorOnADeploymentIsNamedWithoutTheirClaims(t *testing.T) {
	s := setup(t).scene(t)
	stored := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)

	got, err := s.svc.GetDeployment(context.Background(), apiv2.GetDeploymentRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Actor.ID != "ci@example.com" || got.Actor.Type != string(identity.PrincipalService) {
		t.Errorf("actor = %+v", got.Actor)
	}
}

func TestDeploymentsAreListedForOneEnvironment(t *testing.T) {
	s := setup(t).scene(t)
	p := s.plan(t, oneApply(), allowed())
	s.deployment(t, p.ID)
	s.deployment(t, p.ID)

	got, err := s.svc.ListDeployments(context.Background(), apiv2.ListDeploymentsRequest{
		EnvironmentID: string(s.environment.ID),
	})
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(got.Deployments) != 2 {
		t.Fatalf("got %d deployments, want 2", len(got.Deployments))
	}
	for _, d := range got.Deployments {
		if d.EnvironmentID != string(s.environment.ID) {
			t.Errorf("deployment %q is in %q", d.ID, d.EnvironmentID)
		}
	}
}

func TestListingDeploymentsNeedsAnEnvironmentToListThemFor(t *testing.T) {
	w := setup(t)

	_, err := w.svc.ListDeployments(context.Background(), apiv2.ListDeploymentsRequest{})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestADeploymentNobodyStartedIsNotFound(t *testing.T) {
	s := setup(t).scene(t)
	absent := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)

	_, err := setup(t).svc.GetDeployment(context.Background(), apiv2.GetDeploymentRequest{ID: string(absent.ID)})

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestADeploymentIDOfTheWrongKindIsTheCallersMistake(t *testing.T) {
	w := setup(t)

	_, err := w.svc.GetDeployment(context.Background(), apiv2.GetDeploymentRequest{ID: "pln_0199a0dd-0000-7000-8000-000000000000"})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestAPlanSaysWhatItWouldChangeAndWhetherPolicyAllowsIt(t *testing.T) {
	s := setup(t).scene(t)
	stored := s.plan(t, oneApply(plan.Change{Path: "spec.replicas", From: "2", To: "4"}), allowed())

	got, err := s.svc.GetPlan(context.Background(), apiv2.GetPlanRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if len(got.Operations) != 1 {
		t.Fatalf("got %d operations, want 1", len(got.Operations))
	}
	op := got.Operations[0]
	if op.Target != "api" || op.Kind != string(plan.OperationApply) || !op.Reversible {
		t.Errorf("operation = %+v", op)
	}
	if len(op.Changes) != 1 || op.Changes[0].To != "4" {
		t.Errorf("changes = %+v", op.Changes)
	}
	if !got.Policy.Allowed {
		t.Error("policy says not allowed")
	}
	if got.Policy.Risk.Level != string(policy.RiskLow) {
		t.Errorf("risk = %q", got.Policy.Risk.Level)
	}
}

func TestAPlanCarriesTheHashAnApprovalBindsTo(t *testing.T) {
	s := setup(t).scene(t)
	stored := s.plan(t, oneApply(), allowed())

	got, err := s.svc.GetPlan(context.Background(), apiv2.GetPlanRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Hash != stored.Hash.String() {
		t.Errorf("hash = %q, want %q", got.Hash, stored.Hash)
	}
	if got.BaseRevision != uint64(s.environment.Revision) {
		t.Errorf("base revision = %d, want %d", got.BaseRevision, s.environment.Revision)
	}
	if !got.ExpiresAt.After(got.CreatedAt) {
		t.Error("a plan that does not expire never goes stale, which defeats planning separately from applying")
	}
}

func TestASensitiveChangeIsShownAsChangedAndNotAsWhatItChangedTo(t *testing.T) {
	s := setup(t).scene(t)
	stored := s.plan(t, oneApply(
		plan.Change{Path: "spec.replicas", From: "2", To: "4"},
		plan.Change{Path: "env.DATABASE_PASSWORD", From: "hunter2", To: "correct-horse", Sensitive: true},
	), allowed())

	got, err := s.svc.GetPlan(context.Background(), apiv2.GetPlanRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	var secret apiv2.Change
	for _, c := range got.Operations[0].Changes {
		if c.Path == "env.DATABASE_PASSWORD" {
			secret = c
		}
	}
	if !secret.Sensitive {
		t.Fatal("the sensitive change is not marked; a reader cannot tell the blank is deliberate")
	}
	if secret.From != "" || secret.To != "" {
		t.Errorf("sensitive change = %q -> %q, want both blank", secret.From, secret.To)
	}
	if got.Operations[0].Changes[0].To != "4" {
		t.Error("redaction took the ordinary change with it")
	}
}

func TestAPolicyRefusalExplainsItselfRatherThanJustSayingNo(t *testing.T) {
	s := setup(t).scene(t)
	stored := s.plan(t, oneApply(), policy.Decision{
		Allowed: false,
		Requirements: []policy.Requirement{{
			Type:   policy.RequireApproval,
			Role:   "release-manager",
			Count:  2,
			Detail: "production requires two approvals",
		}},
		Reasons: []policy.Reason{{Code: "approval_required", Message: "production requires two approvals"}},
		Risk: policy.RiskAssessment{
			Level:   policy.RiskHigh,
			Score:   0.8,
			Factors: []policy.RiskFactor{{Code: "production", Message: "the environment is production"}},
		},
	})

	got, err := s.svc.GetPlan(context.Background(), apiv2.GetPlanRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	if got.Policy.Allowed {
		t.Fatal("policy says allowed")
	}
	if len(got.Policy.Requirements) != 1 || got.Policy.Requirements[0].Count != 2 {
		t.Errorf("requirements = %+v", got.Policy.Requirements)
	}
	if len(got.Policy.Reasons) != 1 || got.Policy.Reasons[0].Code != "approval_required" {
		t.Errorf("reasons = %+v", got.Policy.Reasons)
	}
	if len(got.Policy.Risk.Factors) != 1 || got.Policy.Risk.Factors[0].Code != "production" {
		t.Errorf("risk factors = %+v", got.Policy.Risk.Factors)
	}
}

func TestAPlanNobodyComputedIsNotFound(t *testing.T) {
	s := setup(t).scene(t)
	absent := s.plan(t, oneApply(), allowed())

	_, err := setup(t).svc.GetPlan(context.Background(), apiv2.GetPlanRequest{ID: string(absent.ID)})

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestAPlanIDOfTheWrongKindIsTheCallersMistake(t *testing.T) {
	w := setup(t)

	_, err := w.svc.GetPlan(context.Background(), apiv2.GetPlanRequest{ID: "dep_0199a0dd-0000-7000-8000-000000000000"})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestDeploymentsComeBackAPageAtATime(t *testing.T) {
	s := setup(t).scene(t)
	p := s.plan(t, oneApply(), allowed())
	for range 4 {
		s.deployment(t, p.ID)
	}

	got, err := s.svc.ListDeployments(context.Background(), apiv2.ListDeploymentsRequest{
		EnvironmentID: string(s.environment.ID),
		Page:          page.Request{Size: 3},
	})
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}
	if len(got.Deployments) != 3 || got.Next == "" {
		t.Fatalf("got %d deployments and cursor %q, want 3 and a cursor", len(got.Deployments), got.Next)
	}
}

func TestADeploymentThatRanSaysWhenAndWhatItReplaced(t *testing.T) {
	s := setup(t).scene(t)
	p := s.plan(t, oneApply(), allowed())
	first := s.deployment(t, p.ID)
	second := s.deployment(t, p.ID)
	second.Previous = &first.ID

	started := at.Add(time.Minute)
	finished := at.Add(5 * time.Minute)
	for _, step := range []struct {
		status deployment.Status
		when   time.Time
	}{
		{deployment.StatusQueued, at},
		{deployment.StatusApplying, started},
		{deployment.StatusVerifying, finished},
		{deployment.StatusSucceeded, finished},
	} {
		moved, err := second.TransitionTo(step.status, step.when)
		if err != nil {
			t.Fatalf("TransitionTo(%s): %v", step.status, err)
		}
		rev, err := s.store.Deployments().Update(context.Background(), moved)
		if err != nil {
			t.Fatalf("Deployments.Update: %v", err)
		}
		moved.Revision = rev
		second = moved
	}

	got, err := s.svc.GetDeployment(context.Background(), apiv2.GetDeploymentRequest{ID: string(second.ID)})
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Errorf("started at = %v, want %s", got.StartedAt, started)
	}
	if got.FinishedAt == nil || !got.FinishedAt.Equal(finished) {
		t.Errorf("finished at = %v, want %s", got.FinishedAt, finished)
	}
	if got.Previous != string(first.ID) {
		t.Errorf("previous = %q, want %q", got.Previous, first.ID)
	}
}

func TestAnOperationNamesWhatMustHappenBeforeIt(t *testing.T) {
	s := setup(t).scene(t)
	stored := s.plan(t, []plan.PlannedOperation{
		{ID: "migrate", Target: "db", Kind: plan.OperationApply, Summary: "run the schema migration"},
		{
			ID:           "roll",
			Target:       "api",
			Kind:         plan.OperationApply,
			Summary:      "roll the api deployment to 2.0.0",
			Dependencies: []plan.OperationID{"migrate"},
			Reversible:   true,
		},
	}, allowed())

	got, err := s.svc.GetPlan(context.Background(), apiv2.GetPlanRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetPlan: %v", err)
	}
	var roll apiv2.PlannedOperation
	for _, op := range got.Operations {
		if op.ID == "roll" {
			roll = op
		}
	}
	if len(roll.Dependencies) != 1 || roll.Dependencies[0] != "migrate" {
		t.Errorf("dependencies = %v; an applier that cannot see the ordering will run them in parallel", roll.Dependencies)
	}
}
