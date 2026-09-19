package deploy_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/release"
	"go.klarlabs.de/rollops/internal/store/memory"
)

var start = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// movableClock is the one clock in the suite that can be wound forward, which
// is how a plan is made to expire without sleeping.
type movableClock struct{ now time.Time }

func (c *movableClock) Now() time.Time { return c.now }

type stubPlanner struct {
	proposal deploy.Proposal
	err      error
	seen     deploy.PlanRequest
	calls    int
}

func (p *stubPlanner) PlanDeployment(_ context.Context, req deploy.PlanRequest) (deploy.Proposal, error) {
	p.calls++
	p.seen = req
	return p.proposal, p.err
}

type stubPolicy struct {
	decision policy.Decision
	err      error
	seen     deploy.PolicyRequest
}

func (p *stubPolicy) Evaluate(_ context.Context, req deploy.PolicyRequest) (policy.Decision, error) {
	p.seen = req
	return p.decision, p.err
}

func allowed() policy.Decision {
	return policy.Decision{
		Allowed: true,
		Risk: policy.RiskAssessment{
			Level:   policy.RiskMedium,
			Score:   0.4,
			Factors: []policy.RiskFactor{{Code: "production_environment", Message: "targets production"}},
		},
	}
}

func operations() []plan.PlannedOperation {
	return []plan.PlannedOperation{{
		ID:         "op_1",
		Target:     "primary",
		Kind:       plan.OperationApply,
		Summary:    "Update api image digest",
		Reversible: true,
		Diff: plan.Diff{Changes: []plan.Change{
			{Path: "spec.template.spec.containers[0].image", From: "app@sha256:aa", To: "app@sha256:bb"},
		}},
	}}
}

func actor() identity.Principal {
	return identity.Principal{
		ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada",
		Claims: map[string]string{"token": "s3cret", "email": "ada@example.com"},
	}
}

type harness struct {
	store   *memory.Store
	gen     identity.Generator
	clock   *movableClock
	planner *stubPlanner
	policy  *stubPolicy
	service *deploy.Service
	env     environment.Environment
	release release.Release
	project project.Project
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	gen := identity.NewSequenceGenerator()
	clk := &movableClock{now: start}
	store := memory.New()

	proj, err := project.New(gen, clk, project.Project{Name: "checkout"})
	if err != nil {
		t.Fatalf("build project: %v", err)
	}
	if err := store.Projects().Create(ctx, proj); err != nil {
		t.Fatalf("store project: %v", err)
	}

	env, err := environment.New(gen, environment.Environment{
		ProjectID: proj.ID,
		Name:      "production",
		Kind:      environment.KindProduction,
		Targets:   []environment.TargetBinding{{Name: "primary", Driver: "kubernetes"}},
	})
	if err != nil {
		t.Fatalf("build environment: %v", err)
	}
	if err := store.Environments().Create(ctx, env); err != nil {
		t.Fatalf("store environment: %v", err)
	}

	d := digest.Of([]byte("app"))
	art, err := artifact.New(gen, clk, artifact.Artifact{
		ProjectID: proj.ID,
		Kind:      artifact.KindOCIImage,
		Digest:    d,
		Locator:   "oci://registry.example/app@" + d.String(),
		Size:      4096,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
	})
	if err != nil {
		t.Fatalf("build artifact: %v", err)
	}
	if err := store.Artifacts().Create(ctx, art); err != nil {
		t.Fatalf("store artifact: %v", err)
	}

	rel, err := release.New(gen, clk, actor(), release.Release{
		ProjectID: proj.ID,
		Version:   "1.4.0",
		Artifacts: []release.Artifact{{ArtifactID: art.ID, Role: "app"}},
		Source: provenance.SourceRevision{
			Provider:   provenance.ProviderGit,
			Repository: "klarlabs/rollops",
			Revision:   "5f2a1c9e7b3d4a6f8e0c2b4d6a8f0c2e4b6d8a0f",
			Ref:        "refs/heads/main",
		},
	})
	if err != nil {
		t.Fatalf("build release: %v", err)
	}
	if err := store.Releases().Create(ctx, rel); err != nil {
		t.Fatalf("store release: %v", err)
	}

	planner := &stubPlanner{proposal: deploy.Proposal{Operations: operations()}}
	pol := &stubPolicy{decision: allowed()}

	svc, err := deploy.New(deploy.Config{
		Transactor:   store,
		Plans:        store.Plans(),
		Deployments:  store.Deployments(),
		Releases:     store.Releases(),
		Environments: store.Environments(),
		Planner:      planner,
		Policy:       pol,
		Clock:        clk,
		IDs:          gen,
		PlanLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	stored, err := store.Environments().Get(ctx, env.ID)
	if err != nil {
		t.Fatalf("read environment back: %v", err)
	}

	return &harness{
		store: store, gen: gen, clock: clk, planner: planner, policy: pol,
		service: svc, env: stored, release: rel, project: proj,
	}
}

func (h *harness) plan(t *testing.T) plan.DeploymentPlan {
	t.Helper()
	p, err := h.service.Plan(context.Background(), deploy.PlanCommand{
		EnvironmentID: h.env.ID,
		ReleaseID:     h.release.ID,
		Strategy:      deployment.StrategyCanary,
		Actor:         actor(),
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	return p
}

func (h *harness) apply(t *testing.T, p plan.DeploymentPlan) deployment.Deployment {
	t.Helper()
	d, err := h.service.Apply(context.Background(), deploy.ApplyCommand{
		PlanID:  p.ID,
		Trigger: deployment.Trigger{Type: deployment.TriggerManual, Detail: "ship it"},
		Actor:   actor(),
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return d
}

func TestAServiceReportsWhatItWasNotGiven(t *testing.T) {
	_, err := deploy.New(deploy.Config{})
	if !errors.Is(err, deploy.ErrIncomplete) {
		t.Fatalf("New with nothing gave %v, want ErrIncomplete", err)
	}
	// A lifetime of zero is a missing dependency rather than a default: a plan
	// that never expires defeats the point of planning separately from
	// applying.
	for _, name := range []string{"planner", "plan lifetime", "transactor"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not mention the missing %s: %s", name, err)
		}
	}
}

func TestPlanningStoresAPlanThatVerifies(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)

	if err := p.VerifyHash(); err != nil {
		t.Errorf("the returned plan does not verify: %v", err)
	}
	stored, err := h.store.Plans().Get(context.Background(), p.ID)
	if err != nil {
		t.Fatalf("read plan back: %v", err)
	}
	if err := stored.VerifyHash(); err != nil {
		t.Errorf("the stored plan does not verify: %v", err)
	}
	if stored.Hash != p.Hash {
		t.Errorf("stored hash %s, returned %s", stored.Hash, p.Hash)
	}
}

// The revision is what makes an apply against a moved world refusable. If the
// plan were pinned to anything else, CheckApplicable would be comparing two
// numbers that never disagree.
func TestAPlanIsPinnedToTheEnvironmentItWasBuiltFor(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)

	if p.BaseRevision != h.env.Revision {
		t.Errorf("BaseRevision = %d, want the environment's %d", p.BaseRevision, h.env.Revision)
	}
	if p.EnvironmentID != h.env.ID {
		t.Errorf("EnvironmentID = %s, want %s", p.EnvironmentID, h.env.ID)
	}
	if p.ReleaseID != h.release.ID {
		t.Errorf("ReleaseID = %s, want %s", p.ReleaseID, h.release.ID)
	}
	if p.ProjectID != h.project.ID {
		t.Errorf("ProjectID = %s, want %s", p.ProjectID, h.project.ID)
	}
}

func TestPlanningCarriesThePolicyDecisionIntoThePlan(t *testing.T) {
	h := newHarness(t)
	h.policy.decision = policy.Decision{
		Allowed:      true,
		Requirements: []policy.Requirement{{Type: policy.RequireApproval, Role: "sre", Count: 2}},
		Risk:         allowed().Risk,
	}
	p := h.plan(t)

	if len(p.Policy.Requirements) != 1 || p.Policy.Requirements[0].Count != 2 {
		t.Fatalf("the plan does not carry the requirement: %+v", p.Policy.Requirements)
	}
	if p.Policy.Satisfied() {
		t.Error("a plan with an outstanding approval reported itself satisfied")
	}
}

// Policy is written against what would change, not only against where it would
// change. A decision made without the operations is a decision about a
// different question.
func TestPolicySeesTheOperationsItIsRulingOn(t *testing.T) {
	h := newHarness(t)
	h.plan(t)

	if len(h.policy.seen.Operations) != 1 {
		t.Fatalf("policy saw %d operations, want 1", len(h.policy.seen.Operations))
	}
	if h.policy.seen.Environment.ID != h.env.ID {
		t.Error("policy was not told which environment")
	}
	if h.policy.seen.Release.ID != h.release.ID {
		t.Error("policy was not told which release")
	}
	if h.policy.seen.Strategy != deployment.StrategyCanary {
		t.Errorf("policy saw strategy %q", h.policy.seen.Strategy)
	}
}

func TestAReleaseCannotBePlannedIntoAnotherProjectsEnvironment(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	gen := h.gen

	stranger, err := project.New(gen, h.clock, project.Project{Name: "billing"})
	if err != nil {
		t.Fatalf("build project: %v", err)
	}
	if err := h.store.Projects().Create(ctx, stranger); err != nil {
		t.Fatalf("store project: %v", err)
	}

	other, err := environment.New(gen, environment.Environment{
		ProjectID: stranger.ID,
		Name:      "staging",
		Kind:      environment.KindStaging,
		Targets:   []environment.TargetBinding{{Name: "primary", Driver: "kubernetes"}},
	})
	if err != nil {
		t.Fatalf("build environment: %v", err)
	}
	if err := h.store.Environments().Create(ctx, other); err != nil {
		t.Fatalf("store environment: %v", err)
	}

	_, err = h.service.Plan(ctx, deploy.PlanCommand{
		EnvironmentID: other.ID,
		ReleaseID:     h.release.ID,
		Strategy:      deployment.StrategyRolling,
		Actor:         actor(),
	})
	if !errors.Is(err, deploy.ErrCrossProject) {
		t.Fatalf("planning across projects gave %v, want ErrCrossProject", err)
	}
	if h.planner.calls != 0 {
		t.Error("the planner was asked to work out a plan that could never apply")
	}
}

func TestAnEnvironmentWithNoTargetCannotBePlannedFor(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	gen := identity.NewSequenceGenerator()

	bare, err := environment.New(gen, environment.Environment{
		ProjectID: h.project.ID,
		Name:      "not-built-yet",
		Kind:      environment.KindPreview,
	})
	if err != nil {
		t.Fatalf("build environment: %v", err)
	}
	if err := h.store.Environments().Create(ctx, bare); err != nil {
		t.Fatalf("store environment: %v", err)
	}

	_, err = h.service.Plan(ctx, deploy.PlanCommand{
		EnvironmentID: bare.ID,
		ReleaseID:     h.release.ID,
		Strategy:      deployment.StrategyRolling,
		Actor:         actor(),
	})
	if !errors.Is(err, deploy.ErrNoTarget) {
		t.Fatalf("planning into a targetless environment gave %v, want ErrNoTarget", err)
	}
}

func TestPlanningAnUnknownReleaseOrEnvironmentIsNotFound(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	_, err := h.service.Plan(ctx, deploy.PlanCommand{
		EnvironmentID: "env_01JBQ0000000000000000000",
		ReleaseID:     h.release.ID,
		Strategy:      deployment.StrategyRolling,
		Actor:         actor(),
	})
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("an unknown environment gave %v, want ErrNotFound", err)
	}

	_, err = h.service.Plan(ctx, deploy.PlanCommand{
		EnvironmentID: h.env.ID,
		ReleaseID:     "rel_01JBQ0000000000000000000",
		Strategy:      deployment.StrategyRolling,
		Actor:         actor(),
	})
	if !errors.Is(err, port.ErrNotFound) {
		t.Errorf("an unknown release gave %v, want ErrNotFound", err)
	}
}

func TestPlanningPropagatesAPlannerFailure(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("cluster unreachable")
	h.planner.err = boom

	_, err := h.service.Plan(context.Background(), deploy.PlanCommand{
		EnvironmentID: h.env.ID,
		ReleaseID:     h.release.ID,
		Strategy:      deployment.StrategyRolling,
		Actor:         actor(),
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Plan gave %v, want the planner's error", err)
	}
}

func TestPlanningPropagatesAPolicyFailure(t *testing.T) {
	h := newHarness(t)
	boom := errors.New("policy bundle missing")
	h.policy.err = boom

	_, err := h.service.Plan(context.Background(), deploy.PlanCommand{
		EnvironmentID: h.env.ID,
		ReleaseID:     h.release.ID,
		Strategy:      deployment.StrategyRolling,
		Actor:         actor(),
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Plan gave %v, want the policy engine's error", err)
	}
}

// A plan with no operations would apply nothing while reading as a deployment.
func TestAPlanThatWouldChangeNothingIsRefused(t *testing.T) {
	h := newHarness(t)
	h.planner.proposal = deploy.Proposal{}

	_, err := h.service.Plan(context.Background(), deploy.PlanCommand{
		EnvironmentID: h.env.ID,
		ReleaseID:     h.release.ID,
		Strategy:      deployment.StrategyRolling,
		Actor:         actor(),
	})
	if err == nil {
		t.Fatal("a plan with no operations was stored")
	}
}

func TestApplyingQueuesTheDeployment(t *testing.T) {
	h := newHarness(t)
	d := h.apply(t, h.plan(t))

	if d.Status != deployment.StatusQueued {
		t.Errorf("Status = %s, want queued", d.Status)
	}
	// Apply admits; it does not start. A start time here would make every
	// deployment look as though it began the moment it was requested.
	if d.StartedAt != nil {
		t.Errorf("StartedAt = %s, want nil until the engine begins applying", d.StartedAt)
	}
	if d.FinishedAt != nil {
		t.Errorf("FinishedAt = %s, want nil", d.FinishedAt)
	}
	if !d.CreatedAt.Equal(start) {
		t.Errorf("CreatedAt = %s, want %s", d.CreatedAt, start)
	}
}

// The strategy and the operations come from the plan, never from the caller:
// applying a plan reviewed as a canary by any other means is the substitution
// the hash exists to prevent.
func TestTheDeploymentInheritsTheStrategyFromThePlan(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)
	d := h.apply(t, p)

	if d.Strategy != deployment.StrategyCanary {
		t.Errorf("Strategy = %q, want the plan's canary", d.Strategy)
	}
	if d.PlanID != p.ID {
		t.Errorf("PlanID = %s, want %s", d.PlanID, p.ID)
	}
	if d.ReleaseID != p.ReleaseID || d.EnvironmentID != p.EnvironmentID {
		t.Error("the deployment does not name what the plan named")
	}
}

func TestAPlanWithUnmetRequirementsWaitsForApproval(t *testing.T) {
	h := newHarness(t)
	h.policy.decision = policy.Decision{
		Allowed:      true,
		Requirements: []policy.Requirement{{Type: policy.RequireApproval, Role: "sre", Count: 1}},
		Risk:         allowed().Risk,
	}
	d := h.apply(t, h.plan(t))

	if d.Status != deployment.StatusAwaitingApproval {
		t.Errorf("Status = %s, want awaiting_approval", d.Status)
	}
}

func TestARefusedPlanIsNotApplied(t *testing.T) {
	h := newHarness(t)
	h.policy.decision = policy.Decision{
		Allowed: false,
		Reasons: []policy.Reason{{Code: "change_freeze", Message: "a freeze is in force"}},
		Risk:    allowed().Risk,
	}
	p := h.plan(t)

	_, err := h.service.Apply(context.Background(), deploy.ApplyCommand{
		PlanID:  p.ID,
		Trigger: deployment.Trigger{Type: deployment.TriggerManual},
		Actor:   actor(),
	})
	if !errors.Is(err, deploy.ErrPolicyRefused) {
		t.Fatalf("applying a refused plan gave %v, want ErrPolicyRefused", err)
	}
	// The refusal has to say what to do about it, or an operator is left with
	// nothing but the fact that it happened.
	if !strings.Contains(err.Error(), "change_freeze") {
		t.Errorf("the refusal does not carry policy's reason: %s", err)
	}
	if _, err := h.store.Deployments().FindActive(context.Background(), h.env.ID); !errors.Is(err, port.ErrNotFound) {
		t.Error("a refused plan left a deployment behind")
	}
}

func TestAnExpiredPlanIsNotApplied(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)
	h.clock.now = start.Add(time.Hour)

	_, err := h.service.Apply(context.Background(), deploy.ApplyCommand{
		PlanID: p.ID, Trigger: deployment.Trigger{Type: deployment.TriggerManual}, Actor: actor(),
	})
	if !errors.Is(err, plan.ErrPlanExpired) {
		t.Fatalf("applying an expired plan gave %v, want ErrPlanExpired", err)
	}
}

func TestAPlanIsNotAppliedAfterTheEnvironmentMoves(t *testing.T) {
	h := newHarness(t)
	p := h.plan(t)

	ctx := context.Background()
	moved := h.env
	moved.Labels = map[string]string{"tier": "gold"}
	if _, err := h.store.Environments().Update(ctx, moved); err != nil {
		t.Fatalf("update environment: %v", err)
	}

	_, err := h.service.Apply(ctx, deploy.ApplyCommand{
		PlanID: p.ID, Trigger: deployment.Trigger{Type: deployment.TriggerManual}, Actor: actor(),
	})
	if !errors.Is(err, plan.ErrPlanStale) {
		t.Fatalf("applying against a moved environment gave %v, want ErrPlanStale", err)
	}
}

// Storage is the boundary an edit arrives through, so an edited plan has to be
// caught at apply rather than trusted because the service wrote it. The plan
// here is stored already tampered, which is what an attacker with database
// access leaves behind.
func TestATamperedPlanIsNotApplied(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	p, err := plan.New(identity.NewSequenceGenerator(), h.clock, actor(), time.Hour, plan.DeploymentPlan{
		ProjectID:     h.project.ID,
		EnvironmentID: h.env.ID,
		ReleaseID:     h.release.ID,
		BaseRevision:  h.env.Revision,
		Strategy:      deployment.StrategyRolling,
		Operations:    operations(),
		Policy:        allowed(),
	})
	if err != nil {
		t.Fatalf("build plan: %v", err)
	}
	p.Operations[0].Diff.Changes[0].To = "app@sha256:evil"
	if err := h.store.Plans().Create(ctx, p); err != nil {
		t.Fatalf("store plan: %v", err)
	}

	_, err = h.service.Apply(ctx, deploy.ApplyCommand{
		PlanID: p.ID, Trigger: deployment.Trigger{Type: deployment.TriggerManual}, Actor: actor(),
	})
	if !errors.Is(err, plan.ErrPlanTampered) {
		t.Fatalf("applying a tampered plan gave %v, want ErrPlanTampered", err)
	}
}

func TestAnEnvironmentRunsOneDeploymentAtATime(t *testing.T) {
	h := newHarness(t)
	first := h.apply(t, h.plan(t))

	second := h.plan(t)
	_, err := h.service.Apply(context.Background(), deploy.ApplyCommand{
		PlanID: second.ID, Trigger: deployment.Trigger{Type: deployment.TriggerManual}, Actor: actor(),
	})
	if !errors.Is(err, deploy.ErrEnvironmentBusy) {
		t.Fatalf("a second apply gave %v, want ErrEnvironmentBusy", err)
	}
	if !strings.Contains(err.Error(), string(first.ID)) {
		t.Errorf("the refusal does not name what is in the way: %s", err)
	}
}

// What an environment is running is the last thing that worked. A failed
// deployment is not what a rollback would return to.
func TestADeploymentRecordsTheSucceededOneItReplaces(t *testing.T) {
	h := newHarness(t)

	landed := h.finish(t, h.apply(t, h.plan(t)), deployment.StatusSucceeded)
	next := h.apply(t, h.plan(t))

	if next.Previous == nil {
		t.Fatal("the new deployment does not record what it replaces")
	}
	if *next.Previous != landed.ID {
		t.Errorf("Previous = %s, want %s", *next.Previous, landed.ID)
	}

	h.finish(t, next, deployment.StatusFailed)
	third := h.apply(t, h.plan(t))
	if third.Previous == nil || *third.Previous != landed.ID {
		t.Errorf("a failed deployment became the thing being replaced: %v", third.Previous)
	}
}

// The deployment Apply hands back is the one the engine goes on to transition,
// so it has to carry the revision the store committed at. A copy holding a
// revision the store never agreed to loses the very next compare-and-set, and
// the deployment is stuck at queued with nothing obviously wrong with it.
func TestTheAdmittedDeploymentCanBeAdvancedWithoutRereadingIt(t *testing.T) {
	h := newHarness(t)
	d := h.apply(t, h.plan(t))

	moved, err := d.TransitionTo(deployment.StatusApplying, h.clock.now)
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if _, err := h.store.Deployments().Update(context.Background(), moved); err != nil {
		t.Fatalf("advancing the deployment Apply returned: %v", err)
	}
}

func TestTheFirstDeploymentReplacesNothing(t *testing.T) {
	h := newHarness(t)
	if d := h.apply(t, h.plan(t)); d.Previous != nil {
		t.Errorf("Previous = %s on the first deployment, want nil", *d.Previous)
	}
}

// A deployment is persisted and rendered on every surface, so a credential that
// reached it would be impossible to recall (INV-012). Attribution is the point
// of keeping the actor at all, so the claims that are not credentials survive —
// what must not is the value behind a claim named like a secret.
func TestASecretClaimDoesNotReachTheStoredDeployment(t *testing.T) {
	h := newHarness(t)
	d := h.apply(t, h.plan(t))

	stored, err := h.store.Deployments().Get(context.Background(), d.ID)
	if err != nil {
		t.Fatalf("read deployment back: %v", err)
	}
	for _, got := range []deployment.Deployment{d, stored} {
		if got.Actor.Claims["token"] == "s3cret" {
			t.Errorf("the credential survived onto the deployment: %v", got.Actor.Claims)
		}
		if got.Actor.Claims["email"] != "ada@example.com" {
			t.Errorf("attribution was lost along with the credential: %v", got.Actor.Claims)
		}
		if got.Actor.ID != "u1" {
			t.Errorf("the deployment is attributed to %+v", got.Actor)
		}
	}
}

func TestApplyingAnUnknownPlanIsNotFound(t *testing.T) {
	h := newHarness(t)
	_, err := h.service.Apply(context.Background(), deploy.ApplyCommand{
		PlanID: "pln_01JBQ0000000000000000000",
		Actor:  actor(),
	})
	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("applying an unknown plan gave %v, want ErrNotFound", err)
	}
}

// finish drives a deployment to a terminal status along a legal path, so that
// the test exercises the state machine rather than writing a status past it.
func (h *harness) finish(t *testing.T, d deployment.Deployment, outcome deployment.Status) deployment.Deployment {
	t.Helper()
	ctx := context.Background()
	for _, next := range []deployment.Status{
		deployment.StatusApplying, deployment.StatusVerifying, outcome,
	} {
		moved, err := d.TransitionTo(next, h.clock.now)
		if err != nil {
			t.Fatalf("transition to %s: %v", next, err)
		}
		rev, err := h.store.Deployments().Update(ctx, moved)
		if err != nil {
			t.Fatalf("store %s: %v", next, err)
		}
		moved.Revision = rev
		d = moved
	}
	return d
}
