package planner_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/release"
	"go.klarlabs.de/rollops/internal/engine/planner"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// journal records what the fakes were asked to do, so a test can assert on
// what planning did not do as well as on what it did.
type journal struct{ calls []string }

func (j *journal) note(format string, a ...any) {
	j.calls = append(j.calls, fmt.Sprintf(format, a...))
}

func (j *journal) did(call string) bool { return slices.Contains(j.calls, call) }

// fakeTarget answers Plan with whatever the test set and records every other
// method, so that a call planning is forbidden to make is visible.
type fakeTarget struct {
	name   string
	caps   targetv2.Capabilities
	result targetv2.PlanResult
	err    error
	log    *journal
}

func (f *fakeTarget) Metadata() targetv2.Metadata {
	return targetv2.Metadata{Kind: "fake", Name: f.name, Version: "v0"}
}

func (f *fakeTarget) Capabilities(context.Context) (targetv2.Capabilities, error) {
	return f.caps, nil
}

func (f *fakeTarget) Inspect(context.Context, targetv2.InspectRequest) (targetv2.ObservedState, error) {
	f.log.note("inspect:%s", f.name)
	return targetv2.ObservedState{}, nil
}

func (f *fakeTarget) Plan(_ context.Context, req targetv2.PlanRequest) (targetv2.PlanResult, error) {
	f.log.note("plan:%s:%s", f.name, req.Desired.Checksum)
	return f.result, f.err
}

func (f *fakeTarget) Apply(context.Context, targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	f.log.note("apply:%s", f.name)
	return targetv2.ApplyResult{}, nil
}

func (f *fakeTarget) Observe(context.Context, targetv2.ObserveRequest) (targetv2.Observation, error) {
	f.log.note("observe:%s", f.name)
	return targetv2.Observation{}, nil
}

func (f *fakeTarget) Rollback(context.Context, targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	f.log.note("rollback:%s", f.name)
	return targetv2.RollbackResult{}, nil
}

// stub is the test's Targets and Desired in one value.
type stub struct {
	log     journal
	targets map[string]*fakeTarget
	resolve func(environment.TargetBinding) (*targetv2.Bound, error)
	render  func(planner.DesiredRequest) (targetv2.DesiredState, error)
}

func (s *stub) Resolve(_ context.Context, req planner.TargetRequest) (*targetv2.Bound, error) {
	s.log.note("resolve:%s", req.Binding.Name)
	if s.resolve != nil {
		return s.resolve(req.Binding)
	}
	t, ok := s.targets[req.Binding.Name]
	if !ok {
		return nil, fmt.Errorf("stub: nothing registered for %q", req.Binding.Name)
	}
	return targetv2.NewBound(t, t.caps, targetv2.OnClose(func() error {
		s.log.note("close:%s", req.Binding.Name)
		return nil
	})), nil
}

func (s *stub) DesiredState(_ context.Context, req planner.DesiredRequest) (targetv2.DesiredState, error) {
	s.log.note("render:%s", req.Binding.Name)
	if s.render != nil {
		return s.render(req)
	}
	return targetv2.DesiredState{
		Kind:     "fake",
		Spec:     []byte(req.Release.Version),
		Checksum: req.Release.Version,
	}, nil
}

// changes is a target that has work to do and can undo none of it.
func changes() *fakeTarget {
	return &fakeTarget{result: targetv2.PlanResult{Changes: true}}
}

// settled is a target already at the desired state.
func settled() *fakeTarget { return &fakeTarget{result: targetv2.PlanResult{}} }

// with registers targets under the binding names an environment declares and
// returns a planner wired to them.
func with(t *testing.T, targets map[string]*fakeTarget) (*planner.Planner, *stub) {
	t.Helper()
	s := &stub{targets: targets}
	for name, ft := range targets {
		ft.name, ft.log = name, &s.log
	}
	p, err := planner.New(s, s)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p, s
}

func binding(name string) environment.TargetBinding {
	return environment.TargetBinding{Name: name, Driver: "fake"}
}

func env(names ...string) environment.Environment {
	e := environment.Environment{
		ID:        "env-1",
		ProjectID: "proj-1",
		Name:      "production",
		Kind:      environment.KindProduction,
		Revision:  7,
	}
	for _, n := range names {
		e.Targets = append(e.Targets, binding(n))
	}
	return e
}

func rel() release.Release {
	return release.Release{ID: "rel-new", ProjectID: "proj-1", Version: "2.0.0"}
}

func request(names ...string) deploy.PlanRequest {
	return deploy.PlanRequest{
		Environment: env(names...),
		Release:     rel(),
		Strategy:    deployment.StrategyRolling,
	}
}

func propose(t *testing.T, p *planner.Planner, req deploy.PlanRequest) deploy.Proposal {
	t.Helper()
	got, err := p.PlanDeployment(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanDeployment: %v", err)
	}
	return got
}

func operationIDs(p deploy.Proposal) []string {
	ids := make([]string, 0, len(p.Operations))
	for _, o := range p.Operations {
		ids = append(ids, string(o.ID))
	}
	return ids
}

func TestEachBindingBecomesAnOperationInTheOrderTheEnvironmentDeclares(t *testing.T) {
	p, _ := with(t, map[string]*fakeTarget{
		"api": changes(), "worker": changes(), "web": changes(),
	})

	got := operationIDs(propose(t, p, request("api", "worker", "web")))

	want := []string{"api", "worker", "web"}
	if !slices.Equal(got, want) {
		t.Fatalf("operations %v, want %v", got, want)
	}
}

func TestAnOperationNamesItsBindingAndWhatItWouldDo(t *testing.T) {
	p, _ := with(t, map[string]*fakeTarget{"api": changes()})

	op := propose(t, p, request("api")).Operations[0]

	if op.Target != "api" {
		t.Errorf("target %q, want api", op.Target)
	}
	if op.Kind != plan.OperationApply {
		t.Errorf("kind %q, want %q", op.Kind, plan.OperationApply)
	}
	if !strings.Contains(op.Summary, "2.0.0") || !strings.Contains(op.Summary, "api") {
		t.Errorf("summary %q names neither the release nor the binding", op.Summary)
	}
}

func TestATargetWithNothingToChangeIsLeftOut(t *testing.T) {
	p, _ := with(t, map[string]*fakeTarget{"api": changes(), "worker": settled()})

	got := operationIDs(propose(t, p, request("api", "worker")))

	if want := []string{"api"}; !slices.Equal(got, want) {
		t.Fatalf("operations %v, want %v", got, want)
	}
}

func TestAnEnvironmentAlreadyAtTheReleaseHasNothingToDo(t *testing.T) {
	p, _ := with(t, map[string]*fakeTarget{"api": settled(), "worker": settled()})

	_, err := p.PlanDeployment(context.Background(), request("api", "worker"))

	if !errors.Is(err, planner.ErrNothingToDo) {
		t.Fatalf("err %v, want ErrNothingToDo", err)
	}
}

func TestAnEnvironmentWithNoTargetsHasNothingToDo(t *testing.T) {
	p, _ := with(t, map[string]*fakeTarget{})

	_, err := p.PlanDeployment(context.Background(), request())

	if !errors.Is(err, planner.ErrNothingToDo) {
		t.Fatalf("err %v, want ErrNothingToDo", err)
	}
}

func TestABlockedTargetRefusesTheWholePlan(t *testing.T) {
	blocked := changes()
	blocked.result.Blockers = []string{"the namespace does not exist"}
	p, _ := with(t, map[string]*fakeTarget{"api": changes(), "worker": blocked})

	_, err := p.PlanDeployment(context.Background(), request("api", "worker"))

	if !errors.Is(err, planner.ErrBlocked) {
		t.Fatalf("err %v, want ErrBlocked", err)
	}
	for _, want := range []string{"worker", "the namespace does not exist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestABlockedTargetIsRefusedEvenWithNothingToChange(t *testing.T) {
	// Blockers outrank Changes: a target reporting no work *and* a reason the
	// apply would fail is describing a broken world, not an idle one, and
	// skipping it would plan around the problem instead of surfacing it.
	blocked := settled()
	blocked.result.Blockers = []string{"credentials expired"}
	p, _ := with(t, map[string]*fakeTarget{"api": blocked})

	_, err := p.PlanDeployment(context.Background(), request("api"))

	if !errors.Is(err, planner.ErrBlocked) {
		t.Fatalf("err %v, want ErrBlocked", err)
	}
}

func TestPlanningDoesNotApplyAnything(t *testing.T) {
	// Spec 8.2: planning must not mutate deployment targets.
	p, s := with(t, map[string]*fakeTarget{"api": changes(), "worker": changes()})

	propose(t, p, request("api", "worker"))

	for _, forbidden := range []string{"apply:api", "apply:worker", "rollback:api", "rollback:worker"} {
		if s.log.did(forbidden) {
			t.Errorf("planning called %s", forbidden)
		}
	}
}

func TestTheProviderDiffDoesNotReachThePlan(t *testing.T) {
	// A target's free-form diff is whatever its CLI prints, which for most
	// substrates includes the values of secrets. A plan is persisted and
	// rendered everywhere (INV-012), so the portable summary carries the
	// description and the provider's text is dropped.
	const secret = "postgres://admin:hunter2@db.internal/prod"
	leaky := changes()
	leaky.result.Diff = "- DATABASE_URL: " + secret
	leaky.result.Rendered = []byte(secret)
	p, _ := with(t, map[string]*fakeTarget{"api": leaky})

	got := propose(t, p, request("api"))

	if rendered := fmt.Sprintf("%+v", got); strings.Contains(rendered, "hunter2") {
		t.Fatalf("the proposal carries the provider diff: %s", rendered)
	}
}

func TestATargetThatCanRollBackItselfPlansAReversibleOperation(t *testing.T) {
	native := changes()
	native.caps.NativeRollback = true
	p, _ := with(t, map[string]*fakeTarget{"api": native})

	if op := propose(t, p, request("api")).Operations[0]; !op.Reversible {
		t.Fatal("an operation the target can undo is not marked reversible")
	}
}

func TestATargetThatCannotRollBackItselfIsNotAReversibleOperation(t *testing.T) {
	p, _ := with(t, map[string]*fakeTarget{"api": changes()})
	req := request("api")
	req.Current = &deployment.Deployment{ID: "dep-old", ReleaseID: "rel-old"}

	// There is a release to go back to, but putting it back means rendering and
	// applying it again — which this plan does not contain. The operation is
	// not reversible on its own, whatever the plan as a whole can arrange.
	if op := propose(t, p, req).Operations[0]; op.Reversible {
		t.Fatal("an operation the target cannot undo claims to be reversible")
	}
}

func TestAFirstDeploymentHasNoWayBack(t *testing.T) {
	native := changes()
	native.caps.NativeRollback = true
	p, _ := with(t, map[string]*fakeTarget{"api": native})

	if got := propose(t, p, request("api")).Rollback; got.ToRelease != "" || got.Automatic {
		t.Fatalf("rollback %+v, want none", got)
	}
}

func TestTheRollbackLeadsBackToWhatIsDeployed(t *testing.T) {
	native := changes()
	native.caps.NativeRollback = true
	p, _ := with(t, map[string]*fakeTarget{"api": native})
	req := request("api")
	req.Current = &deployment.Deployment{ID: "dep-old", ReleaseID: "rel-old"}

	got := propose(t, p, req).Rollback

	if got.FromRelease != "rel-new" || got.ToRelease != "rel-old" {
		t.Fatalf("rollback %s -> %s, want rel-new -> rel-old", got.FromRelease, got.ToRelease)
	}
	if !got.Automatic {
		t.Error("every operation is reversible but the rollback is not automatic")
	}
}

func TestRollbackIsAutomaticOnlyWhenEveryOperationCanBeUndone(t *testing.T) {
	native := changes()
	native.caps.NativeRollback = true
	p, _ := with(t, map[string]*fakeTarget{"api": native, "worker": changes()})
	req := request("api", "worker")
	req.Current = &deployment.Deployment{ID: "dep-old", ReleaseID: "rel-old"}

	got := propose(t, p, req).Rollback

	if got.ToRelease != "rel-old" {
		t.Fatalf("rollback leads to %q, want rel-old", got.ToRelease)
	}
	if got.Automatic {
		t.Error("one operation cannot be undone but the rollback is automatic")
	}
}

func TestEveryTargetIsReleasedWhenPlanningSucceeds(t *testing.T) {
	p, s := with(t, map[string]*fakeTarget{"api": changes(), "worker": settled()})

	propose(t, p, request("api", "worker"))

	for _, want := range []string{"close:api", "close:worker"} {
		if !s.log.did(want) {
			t.Errorf("%s never happened; calls: %v", want, s.log.calls)
		}
	}
}

func TestATargetIsReleasedEvenWhenItRefusesToPlan(t *testing.T) {
	broken := changes()
	broken.err = errors.New("the cluster is unreachable")
	p, s := with(t, map[string]*fakeTarget{"api": broken})

	if _, err := p.PlanDeployment(context.Background(), request("api")); err == nil {
		t.Fatal("a target that could not plan was reported as planned")
	}
	if !s.log.did("close:api") {
		t.Errorf("the target was not released; calls: %v", s.log.calls)
	}
}

func TestATargetThatCannotBeResolvedNamesTheBinding(t *testing.T) {
	p, s := with(t, map[string]*fakeTarget{})
	s.resolve = func(environment.TargetBinding) (*targetv2.Bound, error) {
		return nil, errors.New("no driver named fake")
	}

	_, err := p.PlanDeployment(context.Background(), request("api"))

	if err == nil || !strings.Contains(err.Error(), "api") {
		t.Fatalf("err %v, want one naming the binding", err)
	}
}

func TestADesiredStateThatCannotBeRenderedNamesTheBinding(t *testing.T) {
	p, s := with(t, map[string]*fakeTarget{"api": changes()})
	s.render = func(planner.DesiredRequest) (targetv2.DesiredState, error) {
		return targetv2.DesiredState{}, errors.New("the chart has no values for this environment")
	}

	_, err := p.PlanDeployment(context.Background(), request("api"))

	if err == nil || !strings.Contains(err.Error(), "api") {
		t.Fatalf("err %v, want one naming the binding", err)
	}
}

func TestATargetThatFailsToPlanNamesTheBinding(t *testing.T) {
	broken := changes()
	broken.err = errors.New("the cluster is unreachable")
	p, _ := with(t, map[string]*fakeTarget{"api": broken})

	_, err := p.PlanDeployment(context.Background(), request("api"))

	if err == nil || !strings.Contains(err.Error(), "api") {
		t.Fatalf("err %v, want one naming the binding", err)
	}
}

func TestTheDesiredStateIsRenderedForTheReleaseBeingPlanned(t *testing.T) {
	p, s := with(t, map[string]*fakeTarget{"api": changes()})
	var seen planner.DesiredRequest
	s.render = func(req planner.DesiredRequest) (targetv2.DesiredState, error) {
		seen = req
		return targetv2.DesiredState{Kind: "fake", Checksum: "c"}, nil
	}

	propose(t, p, request("api"))

	if seen.Release.ID != "rel-new" {
		t.Errorf("rendered release %q, want rel-new", seen.Release.ID)
	}
	if seen.Binding.Name != "api" {
		t.Errorf("rendered binding %q, want api", seen.Binding.Name)
	}
	if seen.Environment.ID != "env-1" {
		t.Errorf("rendered environment %q, want env-1", seen.Environment.ID)
	}
}

func TestTheRenderedStateIsWhatTheTargetIsAskedToPlan(t *testing.T) {
	p, s := with(t, map[string]*fakeTarget{"api": changes()})

	propose(t, p, request("api"))

	if !s.log.did("plan:api:2.0.0") {
		t.Fatalf("the target was not planned against the rendered state; calls: %v", s.log.calls)
	}
}

func TestTheProposalBuildsAPlanTheDomainAccepts(t *testing.T) {
	p, _ := with(t, map[string]*fakeTarget{"api": changes(), "worker": changes()})
	req := request("api", "worker")
	req.Current = &deployment.Deployment{ID: "dep-old", ReleaseID: "rel-old"}

	got := propose(t, p, req)

	built, err := plan.New(
		identity.NewGenerator(),
		identity.NewFixedClock(time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)),
		identity.Principal{ID: "u-1", Type: identity.PrincipalHuman},
		time.Hour,
		plan.DeploymentPlan{
			ProjectID:     req.Environment.ProjectID,
			EnvironmentID: req.Environment.ID,
			ReleaseID:     req.Release.ID,
			BaseRevision:  req.Environment.Revision,
			Strategy:      req.Strategy,
			Operations:    got.Operations,
			Rollback:      got.Rollback,
			Policy:        policy.Decision{Allowed: true, Risk: policy.RiskAssessment{Level: policy.RiskLow}},
		},
	)
	if err != nil {
		t.Fatalf("the proposal does not make a valid plan: %v", err)
	}
	if err := built.VerifyHash(); err != nil {
		t.Fatalf("VerifyHash: %v", err)
	}
}

func TestAPlannerNeedsBothOfItsPorts(t *testing.T) {
	s := &stub{}
	for _, c := range []struct {
		name    string
		targets planner.Targets
		desired planner.Desired
	}{
		{"no targets", nil, s},
		{"no desired", s, nil},
		{"neither", nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := planner.New(c.targets, c.desired); err == nil {
				t.Fatal("a planner missing a port was built anyway")
			}
		})
	}
}

func TestThePlannerIsADeployPlanner(t *testing.T) {
	p, _ := with(t, map[string]*fakeTarget{})
	var _ deploy.Planner = p
}
