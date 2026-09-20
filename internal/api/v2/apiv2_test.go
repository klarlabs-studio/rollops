package apiv2_test

import (
	"context"
	"errors"
	"testing"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/app/deploy"
	appenv "go.klarlabs.de/rollops/internal/app/environment"
	"go.klarlabs.de/rollops/internal/app/port"
	appproject "go.klarlabs.de/rollops/internal/app/project"
	apprelease "go.klarlabs.de/rollops/internal/app/release"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/release"
	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/store/memory"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

// movableClock is how an idempotency window is made to lapse without
// sleeping.
type movableClock struct{ now time.Time }

func (c *movableClock) Now() time.Time { return c.now }

// stubPlanner and stubPolicy stand in for the two adapters a deploy.Service
// dispatches to. Everything else behind the write side is the real thing:
// these tests are about what the API does with an answer, and a fake
// deploy.Service would let the API disagree with it.
type stubPlanner struct {
	proposal deploy.Proposal
	err      error
	calls    int
}

func (p *stubPlanner) PlanDeployment(context.Context, deploy.PlanRequest) (deploy.Proposal, error) {
	p.calls++
	return p.proposal, p.err
}

type stubPolicy struct {
	decision policy.Decision
	err      error
}

func (p *stubPolicy) Evaluate(context.Context, deploy.PolicyRequest) (policy.Decision, error) {
	return p.decision, p.err
}

// stubDeployer stands where a test never reaches the write side. New refuses a
// nil deployer, and a read test that somehow planned would fail on this error
// rather than quietly pass.
type stubDeployer struct{}

func (stubDeployer) Plan(context.Context, deploy.PlanCommand) (plan.DeploymentPlan, error) {
	return plan.DeploymentPlan{}, errNotWired
}

func (stubDeployer) Apply(context.Context, deploy.ApplyCommand) (deployment.Deployment, error) {
	return deployment.Deployment{}, errNotWired
}

func (stubDeployer) Approve(context.Context, deploy.ApproveCommand) (deployment.Deployment, error) {
	return deployment.Deployment{}, errNotWired
}

func (stubDeployer) Cancel(context.Context, deploy.CancelCommand) (deployment.Deployment, error) {
	return deployment.Deployment{}, errNotWired
}

var errNotWired = errors.New("the deployer was not meant to be called")

type world struct {
	svc       *apiv2.Service
	store     *memory.Store
	ids       identity.Generator
	clock     *movableClock
	deployer  apiv2.Deployer
	proposals *stubPlanner
	decisions *stubPolicy
}

// config is every dependency a service needs, so that a test wanting one
// broken or absent says which rather than repeating the other nine.
func config(store *memory.Store, clock identity.Clock, deployer apiv2.Deployer) apiv2.Config {
	// The real registrar rather than a stub, for the reason stubPlanner names:
	// these tests are about what the API does with an answer, and a fake would
	// let the API disagree with the service it projects.
	registrar, err := apprelease.New(apprelease.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Artifacts:  store.Artifacts(),
		Releases:   store.Releases(),
		Events:     store.Events(),
		Clock:      clock,
		IDs:        identity.NewGenerator(),
	})
	if err != nil {
		panic("apprelease.New: " + err.Error())
	}
	founder, err := appproject.New(appproject.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Events:     store.Events(),
		Clock:      clock,
		IDs:        identity.NewGenerator(),
	})
	if err != nil {
		panic("appproject.New: " + err.Error())
	}
	binder, err := appenv.New(appenv.Config{
		Transactor:   store,
		Projects:     store.Projects(),
		Environments: store.Environments(),
		Events:       store.Events(),
		Clock:        clock,
		IDs:          identity.NewGenerator(),
	})
	if err != nil {
		panic("appenv.New: " + err.Error())
	}
	return apiv2.Config{
		Projects:     store.Projects(),
		Environments: store.Environments(),
		Releases:     store.Releases(),
		Artifacts:    store.Artifacts(),
		Deployments:  store.Deployments(),
		Plans:        store.Plans(),
		Events:       store.Events(),
		Deployer:     deployer,
		Registrar:    registrar,
		Founder:      founder,
		Binder:       binder,
		Keys:         store.Idempotency(),
		Clock:        clock,
	}
}

func setup(t *testing.T) *world {
	t.Helper()
	store := memory.New()
	clock := &movableClock{now: at}
	ids := identity.NewGenerator()
	proposals := &stubPlanner{proposal: deploy.Proposal{Operations: oneApply()}}
	decisions := &stubPolicy{decision: allowed()}

	deployer, err := deploy.New(deploy.Config{
		Transactor:   store,
		Plans:        store.Plans(),
		Deployments:  store.Deployments(),
		Approvals:    store.Approvals(),
		Releases:     store.Releases(),
		Environments: store.Environments(),
		Planner:      proposals,
		Policy:       decisions,
		Clock:        clock,
		IDs:          ids,
		Events:       store.Events(),
		PlanLifetime: time.Hour,
	})
	if err != nil {
		t.Fatalf("deploy.New: %v", err)
	}
	svc, err := apiv2.New(config(store, clock, deployer))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &world{
		svc:       svc,
		store:     store,
		ids:       ids,
		clock:     clock,
		deployer:  deployer,
		proposals: proposals,
		decisions: decisions,
	}
}

func (w *world) project(t *testing.T, name string) project.Project {
	t.Helper()
	p, err := project.New(w.ids, w.clock, project.Project{
		Name:        name,
		Description: name + " service",
		Labels:      map[string]string{"team": "platform"},
	})
	if err != nil {
		t.Fatalf("project.New: %v", err)
	}
	if err := w.store.Projects().Create(context.Background(), p); err != nil {
		t.Fatalf("Projects.Create: %v", err)
	}
	return p
}

func (w *world) environment(t *testing.T, p identity.ProjectID, e environment.Environment) environment.Environment {
	t.Helper()
	e.ProjectID = p
	if e.Kind == "" {
		e.Kind = environment.KindProduction
	}
	env, err := environment.New(w.ids, e)
	if err != nil {
		t.Fatalf("environment.New: %v", err)
	}
	if err := w.store.Environments().Create(context.Background(), env); err != nil {
		t.Fatalf("Environments.Create: %v", err)
	}
	// Read back rather than return what was written: storage assigns the
	// revision, and a plan built against the in-memory copy's zero would be
	// refused as stale the moment anybody tried to apply it.
	stored, err := w.store.Environments().Get(context.Background(), env.ID)
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}
	return stored
}

func codeOf(t *testing.T, err error) apierr.Code {
	t.Helper()
	if err == nil {
		t.Fatal("wanted a failure, got none")
	}
	var e *apierr.Error
	if !errors.As(err, &e) {
		t.Fatalf("error %v is not an *apierr.Error; a transport cannot name it", err)
	}
	return e.Code
}

func TestAProjectIsReturnedByTheIDItWasCreatedWith(t *testing.T) {
	w := setup(t)
	stored := w.project(t, "checkout")

	got, err := w.svc.GetProject(context.Background(), apiv2.GetProjectRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.ID != string(stored.ID) {
		t.Errorf("id = %q, want %q", got.ID, stored.ID)
	}
	if got.Name != "checkout" {
		t.Errorf("name = %q, want checkout", got.Name)
	}
	if got.Description != "checkout service" {
		t.Errorf("description = %q", got.Description)
	}
	if got.Labels["team"] != "platform" {
		t.Errorf("labels = %v", got.Labels)
	}
	if !got.CreatedAt.Equal(at) {
		t.Errorf("created at = %s, want %s", got.CreatedAt, at)
	}
}

func TestAResourceCarriesTheRevisionItWasReadAt(t *testing.T) {
	w := setup(t)
	stored := w.project(t, "checkout")

	got, err := w.svc.GetProject(context.Background(), apiv2.GetProjectRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if got.Revision == 0 {
		t.Fatal("revision is zero; an update built on this read could not be checked for staleness")
	}
}

func TestAnIDThatIsNotAProjectIDIsTheCallersMistake(t *testing.T) {
	w := setup(t)

	_, err := w.svc.GetProject(context.Background(), apiv2.GetProjectRequest{ID: "env_0199a0dd-0000-7000-8000-000000000000"})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s: a well-formed id of the wrong kind is malformed input, not a missing project", got, apierr.InvalidArgument)
	}
}

func TestAProjectNobodyCreatedIsNotFound(t *testing.T) {
	// A well-formed id from one estate, asked of another that never saw it.
	absent := setup(t).project(t, "checkout")

	_, err := setup(t).svc.GetProject(context.Background(), apiv2.GetProjectRequest{ID: string(absent.ID)})

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

// founding is the request every creation test starts from.
func founding() apiv2.CreateProjectRequest {
	return apiv2.CreateProjectRequest{
		Name:        "checkout",
		Description: "the thing that takes the money",
		Labels:      map[string]string{"team": "platform"},
		Actor:       author(),
	}
}

func TestCreatingAProjectReturnsItReadableAtOnce(t *testing.T) {
	w := setup(t)

	got, err := w.svc.CreateProject(context.Background(), founding())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if got.ID == "" {
		t.Fatal("no project id; nothing could be filed under it")
	}
	if got.Name != "checkout" {
		t.Errorf("name = %q, want checkout", got.Name)
	}
	if got.Labels["team"] != "platform" {
		t.Errorf("labels = %v", got.Labels)
	}

	read, err := w.svc.GetProject(context.Background(), apiv2.GetProjectRequest{ID: got.ID})
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if read.ID != got.ID {
		t.Errorf("read back %q, want %q", read.ID, got.ID)
	}
}

// The gap this endpoint closes: every other mutation names a project, and
// until now nothing a caller could reach made one.
func TestAProjectCreatedThroughTheAPICanHoldAnArtifact(t *testing.T) {
	w := setup(t)
	p, err := w.svc.CreateProject(context.Background(), founding())
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	got, err := w.svc.RegisterArtifact(context.Background(), registering(identity.ProjectID(p.ID), "api"))
	if err != nil {
		t.Fatalf("RegisterArtifact: %v", err)
	}
	if got.ProjectID != p.ID {
		t.Errorf("artifact filed under %q, want %q", got.ProjectID, p.ID)
	}
}

func TestAMalformedProjectRequestIsTheCallersMistake(t *testing.T) {
	tests := []struct {
		name string
		edit func(*apiv2.CreateProjectRequest)
	}{
		{"no name", func(r *apiv2.CreateProjectRequest) { r.Name = "" }},
		{"not an alias", func(r *apiv2.CreateProjectRequest) { r.Name = "Check Out!" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := setup(t)
			req := founding()
			tt.edit(&req)

			_, err := w.svc.CreateProject(context.Background(), req)

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

// A name is how a project is addressed on every surface, so a second one
// asking for it is an ambiguity the caller has to resolve — not a failure to
// report.
func TestASecondProjectTakingANameIsAConflict(t *testing.T) {
	w := setup(t)
	if _, err := w.svc.CreateProject(context.Background(), founding()); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	_, err := w.svc.CreateProject(context.Background(), founding())

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// Which is exactly why the key matters here. Without one a retry is
// indistinguishable from that second caller, and a client that never learned
// whether its first call landed is told the name is taken — by itself.
func TestTwoCreateProjectCallsWithOneKeyOpenOneProject(t *testing.T) {
	w := setup(t)
	req := founding()
	req.IdempotencyKey = "k1"

	first, err := w.svc.CreateProject(context.Background(), req)
	if err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}
	second, err := w.svc.CreateProject(context.Background(), req)
	if err != nil {
		t.Fatalf("second CreateProject: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("second call = %q, want the %q the first opened", second.ID, first.ID)
	}
	all, err := w.svc.ListProjects(context.Background(), apiv2.ListProjectsRequest{})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(all.Projects) != 1 {
		t.Errorf("%d projects, want 1", len(all.Projects))
	}
}

func TestAProjectKeyReusedForADifferentNameIsRefused(t *testing.T) {
	w := setup(t)
	req := founding()
	req.IdempotencyKey = "k1"
	if _, err := w.svc.CreateProject(context.Background(), req); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	other := req
	other.Name = "billing"
	_, err := w.svc.CreateProject(context.Background(), other)

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// The description and the labels are outside the fingerprint. A client that
// retries having corrected its prose is retrying, not asking for a different
// project, and refusing it would leave them unable to retry at all.
func TestAProjectKeyRetriedWithBetterProseStillReplays(t *testing.T) {
	w := setup(t)
	req := founding()
	req.IdempotencyKey = "k1"
	first, err := w.svc.CreateProject(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	again := req
	again.Description = "the thing that takes the payments"
	again.Labels = map[string]string{"team": "payments"}
	second, err := w.svc.CreateProject(context.Background(), again)
	if err != nil {
		t.Fatalf("second CreateProject: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("second call = %q, want the %q the first opened", second.ID, first.ID)
	}
	if second.Description != first.Description {
		t.Errorf("description = %q, want the recorded %q: a replay returns what was created, not what was asked for the second time", second.Description, first.Description)
	}
}

func TestAnEnvironmentIsReturnedByItsID(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	stored := w.environment(t, p.ID, environment.Environment{
		Name: "production",
		Kind: environment.KindProduction,
	})

	got, err := w.svc.GetEnvironment(context.Background(), apiv2.GetEnvironmentRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if got.ID != string(stored.ID) {
		t.Errorf("id = %q, want %q", got.ID, stored.ID)
	}
	if got.ProjectID != string(p.ID) {
		t.Errorf("project id = %q, want %q", got.ProjectID, p.ID)
	}
	if got.Name != "production" || got.Kind != string(environment.KindProduction) {
		t.Errorf("environment = %q/%q", got.Name, got.Kind)
	}
}

func TestATargetIsNamedWithoutTheCredentialsItIsConfiguredWith(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	stored := w.environment(t, p.ID, environment.Environment{
		Name: "production",
		Targets: []environment.TargetBinding{{
			Name:   "api",
			Driver: "kubernetes",
			Config: map[string]value.Ref{
				"namespace":  value.Literal("payments"),
				"kubeconfig": value.Secret("prod-kubeconfig"),
			},
		}},
	})

	got, err := w.svc.GetEnvironment(context.Background(), apiv2.GetEnvironmentRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(got.Targets))
	}
	tgt := got.Targets[0]
	if tgt.Name != "api" || tgt.Driver != "kubernetes" {
		t.Errorf("target = %q/%q", tgt.Name, tgt.Driver)
	}
	wantKeys := []string{"kubeconfig", "namespace"}
	if len(tgt.Config) != len(wantKeys) {
		t.Fatalf("config keys = %v, want %v", tgt.Config, wantKeys)
	}
	for i, k := range wantKeys {
		if tgt.Config[i] != k {
			t.Errorf("config key %d = %q, want %q", i, tgt.Config[i], k)
		}
	}
}

func TestAnEnvironmentVariableIsNamedWithoutItsValue(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	stored := w.environment(t, p.ID, environment.Environment{
		Name: "production",
		Variables: map[string]value.Ref{
			"DATABASE_URL": value.Secret("prod-dsn"),
			"REGION":       value.Literal("eu-central-1"),
		},
	})

	got, err := w.svc.GetEnvironment(context.Background(), apiv2.GetEnvironmentRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	want := []string{"DATABASE_URL", "REGION"}
	if len(got.Variables) != len(want) {
		t.Fatalf("variables = %v, want %v", got.Variables, want)
	}
	for i, n := range want {
		if got.Variables[i] != n {
			t.Errorf("variable %d = %q, want %q", i, got.Variables[i], n)
		}
	}
}

func TestEnvironmentsAreListedForOneProject(t *testing.T) {
	w := setup(t)
	mine := w.project(t, "checkout")
	theirs := w.project(t, "billing")
	w.environment(t, mine.ID, environment.Environment{Name: "production"})
	w.environment(t, mine.ID, environment.Environment{Name: "staging", Kind: environment.KindStaging})
	w.environment(t, theirs.ID, environment.Environment{Name: "production"})

	got, err := w.svc.ListEnvironments(context.Background(), apiv2.ListEnvironmentsRequest{
		ProjectID: string(mine.ID),
	})
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(got.Environments) != 2 {
		t.Fatalf("got %d environments, want the 2 belonging to checkout", len(got.Environments))
	}
	for _, e := range got.Environments {
		if e.ProjectID != string(mine.ID) {
			t.Errorf("environment %q belongs to %q", e.Name, e.ProjectID)
		}
	}
}

func TestListingEnvironmentsNeedsAProjectToListThemFor(t *testing.T) {
	w := setup(t)

	_, err := w.svc.ListEnvironments(context.Background(), apiv2.ListEnvironmentsRequest{})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestAnEnvironmentNobodyCreatedIsNotFound(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	absent := w.environment(t, p.ID, environment.Environment{Name: "production"})

	_, err := setup(t).svc.GetEnvironment(context.Background(), apiv2.GetEnvironmentRequest{ID: string(absent.ID)})

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestAnEnvironmentReportsThePoliciesInForceForIt(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	stored := w.environment(t, p.ID, environment.Environment{
		Name: "production",
		Policies: []environment.PolicyBinding{{
			Name: "two-person-rule",
			Ref:  "policies/approval.cel",
			Mode: environment.PolicyEnforce,
		}},
		Lifecycle: environment.Lifecycle{TTL: 72 * time.Hour, DeleteOnClose: true},
	})

	got, err := w.svc.GetEnvironment(context.Background(), apiv2.GetEnvironmentRequest{ID: string(stored.ID)})
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if len(got.Policies) != 1 {
		t.Fatalf("got %d policies, want 1", len(got.Policies))
	}
	if got.Policies[0].Name != "two-person-rule" || got.Policies[0].Mode != string(environment.PolicyEnforce) {
		t.Errorf("policy = %+v", got.Policies[0])
	}
	if got.Lifecycle.TTL != 72*time.Hour || !got.Lifecycle.DeleteOnClose {
		t.Errorf("lifecycle = %+v", got.Lifecycle)
	}
}

func TestAnIDThatIsNotAnEnvironmentIDIsTheCallersMistake(t *testing.T) {
	w := setup(t)

	_, err := w.svc.GetEnvironment(context.Background(), apiv2.GetEnvironmentRequest{ID: "nonsense"})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

// binding declares one environment wired to one cluster, configured with a
// literal and a secret. The secret is the point: it has to reach the record and
// appear in nothing a caller reads back.
func binding(p string) apiv2.CreateEnvironmentRequest {
	return apiv2.CreateEnvironmentRequest{
		ProjectID: p,
		Name:      "production",
		Kind:      string(environment.KindProduction),
		Targets: []apiv2.TargetSpec{{
			Name:   "api",
			Driver: "kubernetes",
			Config: map[string]apiv2.Value{
				"namespace":  {Literal: "payments"},
				"kubeconfig": {Secret: "prod/kubeconfig"},
			},
		}},
		Variables: map[string]apiv2.Value{"DATABASE_URL": {Secret: "prod/db"}},
		Labels:    map[string]string{"tier": "critical"},
		Actor:     author(),
	}
}

func TestCreatingAnEnvironmentReturnsItReadableAtOnce(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")

	got, err := w.svc.CreateEnvironment(context.Background(), binding(string(p.ID)))
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if got.ID == "" {
		t.Fatal("no environment id; nothing could be planned against it")
	}
	if got.Name != "production" || got.Kind != string(environment.KindProduction) {
		t.Errorf("environment = %q/%q", got.Name, got.Kind)
	}
	read, err := w.svc.GetEnvironment(context.Background(), apiv2.GetEnvironmentRequest{ID: got.ID})
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if read.ID != got.ID {
		t.Errorf("read back %q, want %q", read.ID, got.ID)
	}
}

// What went in as a secret name comes back as a key and nothing else. The
// write type carries values and the read type does not, which is why they are
// two types rather than one (INV-012).
func TestWhatWasConfiguredComesBackAsKeysWithoutValues(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")

	got, err := w.svc.CreateEnvironment(context.Background(), binding(string(p.ID)))
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("got %d targets, want 1", len(got.Targets))
	}
	for i, k := range []string{"kubeconfig", "namespace"} {
		if got.Targets[0].Config[i] != k {
			t.Errorf("config key %d = %q, want %q", i, got.Targets[0].Config[i], k)
		}
	}
	if len(got.Variables) != 1 || got.Variables[0] != "DATABASE_URL" {
		t.Errorf("variables = %v, want [DATABASE_URL]", got.Variables)
	}
	stored, err := w.store.Environments().Get(context.Background(), identity.EnvironmentID(got.ID))
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}
	if name := stored.Variables["DATABASE_URL"].SecretName(); name != "prod/db" {
		t.Errorf("the record names secret %q, want prod/db: the value has to reach storage even though no read returns it", name)
	}
}

// The gap this endpoint closes: a plan needs somewhere to land, and until now
// nothing a caller could reach declared one.
func TestAnEnvironmentCreatedThroughTheAPICanBePlannedAgainst(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	env, err := w.svc.CreateEnvironment(context.Background(), binding(string(p.ID)))
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	r, err := w.svc.CreateRelease(context.Background(), w.creating(t, p.ID))
	if err != nil {
		t.Fatalf("CreateRelease: %v", err)
	}

	got, err := w.svc.CreatePlan(context.Background(), apiv2.CreatePlanRequest{
		EnvironmentID: env.ID,
		ReleaseID:     r.ID,
		Strategy:      string(deployment.StrategyRolling),
		Actor:         author(),
	})
	if err != nil {
		t.Fatalf("CreatePlan: %v", err)
	}
	if got.EnvironmentID != env.ID {
		t.Errorf("plan lands in %q, want %q", got.EnvironmentID, env.ID)
	}
}

// A value that is both is not a value with a precedence rule; it is a request
// whose author meant one of two things, and guessing which would resolve it
// silently in favour of whichever branch was written first.
func TestAValueThatIsBothALiteralAndASecretIsRefused(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := binding(string(p.ID))
	req.Variables["DATABASE_URL"] = apiv2.Value{Literal: "postgres://localhost", Secret: "prod/db"}

	_, err := w.svc.CreateEnvironment(context.Background(), req)

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

func TestAMalformedEnvironmentRequestIsTheCallersMistake(t *testing.T) {
	tests := []struct {
		name string
		edit func(*apiv2.CreateEnvironmentRequest)
	}{
		{"not a project id", func(r *apiv2.CreateEnvironmentRequest) { r.ProjectID = "nonsense" }},
		{"no name", func(r *apiv2.CreateEnvironmentRequest) { r.Name = "" }},
		{"unknown kind", func(r *apiv2.CreateEnvironmentRequest) { r.Kind = "prodction" }},
		{"two targets with one name", func(r *apiv2.CreateEnvironmentRequest) {
			r.Targets = append(r.Targets, r.Targets[0])
		}},
		{"target with no driver", func(r *apiv2.CreateEnvironmentRequest) { r.Targets[0].Driver = "" }},
		{"target config both ways", func(r *apiv2.CreateEnvironmentRequest) {
			r.Targets[0].Config["namespace"] = apiv2.Value{Literal: "payments", Secret: "prod/ns"}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := setup(t)
			p := w.project(t, "checkout")
			req := binding(string(p.ID))
			tt.edit(&req)

			_, err := w.svc.CreateEnvironment(context.Background(), req)

			if got := codeOf(t, err); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

// An environment is filed under a project, so a project that is not there is a
// missing resource rather than a malformed request.
func TestAnEnvironmentForAProjectNobodyCreatedIsNotFound(t *testing.T) {
	w := setup(t)
	absent, err := identity.NewProjectID(w.ids)
	if err != nil {
		t.Fatalf("NewProjectID: %v", err)
	}

	_, err = w.svc.CreateEnvironment(context.Background(), binding(string(absent)))

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestASecondEnvironmentTakingANameIsAConflict(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	if _, err := w.svc.CreateEnvironment(context.Background(), binding(string(p.ID))); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	_, err := w.svc.CreateEnvironment(context.Background(), binding(string(p.ID)))

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

func TestTwoCreateEnvironmentCallsWithOneKeyDeclareOneEnvironment(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := binding(string(p.ID))
	req.IdempotencyKey = "k1"

	first, err := w.svc.CreateEnvironment(context.Background(), req)
	if err != nil {
		t.Fatalf("first CreateEnvironment: %v", err)
	}
	second, err := w.svc.CreateEnvironment(context.Background(), req)
	if err != nil {
		t.Fatalf("second CreateEnvironment: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("second call = %q, want the %q the first declared", second.ID, first.ID)
	}
	all, err := w.svc.ListEnvironments(context.Background(), apiv2.ListEnvironmentsRequest{
		ProjectID: string(p.ID),
	})
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(all.Environments) != 1 {
		t.Errorf("%d environments, want 1", len(all.Environments))
	}
}

func TestAnEnvironmentKeyReusedForADifferentNameIsRefused(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := binding(string(p.ID))
	req.IdempotencyKey = "k1"
	if _, err := w.svc.CreateEnvironment(context.Background(), req); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	other := req
	other.Name = "staging"
	_, err := w.svc.CreateEnvironment(context.Background(), other)

	if got := codeOf(t, err); got != apierr.Conflict {
		t.Errorf("code = %s, want %s", got, apierr.Conflict)
	}
}

// The targets and the labels are outside the fingerprint. What a key protects
// is a second environment being declared, and it is the project and the name
// that say which environment this is — a retry that came back with the
// kubeconfig corrected is still a retry.
func TestAnEnvironmentKeyRetriedWithACorrectedTargetStillReplays(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	req := binding(string(p.ID))
	req.IdempotencyKey = "k1"
	first, err := w.svc.CreateEnvironment(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	again := binding(string(p.ID))
	again.IdempotencyKey = "k1"
	again.Targets[0].Config["kubeconfig"] = apiv2.Value{Secret: "prod/kubeconfig-v2"}
	again.Labels = map[string]string{"tier": "critical", "owner": "platform"}
	second, err := w.svc.CreateEnvironment(context.Background(), again)
	if err != nil {
		t.Fatalf("second CreateEnvironment: %v", err)
	}

	if second.ID != first.ID {
		t.Errorf("second call = %q, want the %q the first declared", second.ID, first.ID)
	}
	stored, err := w.store.Environments().Get(context.Background(), identity.EnvironmentID(second.ID))
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}
	if name := stored.Targets[0].Config["kubeconfig"].SecretName(); name != "prod/kubeconfig" {
		t.Errorf("target names secret %q, want the recorded prod/kubeconfig: a replay returns what was declared, not what was asked for the second time", name)
	}
}

// The broken repositories below fail their list read with something nothing has
// classified — the shape a driver error arrives in.
type brokenProjects struct {
	port.ProjectRepository
	err error
}

func (b brokenProjects) List(context.Context) ([]project.Project, error) { return nil, b.err }

type brokenEnvironments struct {
	port.EnvironmentRepository
	err error
}

func (b brokenEnvironments) List(context.Context, identity.ProjectID) ([]environment.Environment, error) {
	return nil, b.err
}

// errLeak is the shape a driver error arrives in: unclassified, and naming
// infrastructure the caller has no business knowing about.
var errLeak = errors.New("dial tcp 10.0.0.7:5432: connect: connection refused")

type brokenReleases struct {
	port.ReleaseRepository
	err error
}

func (b brokenReleases) List(context.Context, identity.ProjectID) ([]release.Release, error) {
	return nil, b.err
}

type brokenArtifacts struct {
	port.ArtifactRepository
	err error
}

func (b brokenArtifacts) List(context.Context, identity.ProjectID) ([]artifact.Artifact, error) {
	return nil, b.err
}

type brokenDeployments struct {
	port.DeploymentRepository
	err error
}

func (b brokenDeployments) ListForEnvironment(
	context.Context, identity.EnvironmentID,
) ([]deployment.Deployment, error) {
	return nil, b.err
}

type brokenEvents struct {
	port.EventReader
	err error
}

func (b brokenEvents) ForAggregate(
	context.Context, event.AggregateType, string, port.Page,
) ([]event.Event, error) {
	return nil, b.err
}

// TestNoListHandsTheCallerAnUnclassifiedMessage states the rule once for every
// list read rather than per endpoint: whatever storage says on the way out, the
// caller gets INTERNAL and nothing about the estate.
func TestNoListHandsTheCallerAnUnclassifiedMessage(t *testing.T) {
	for _, tc := range []struct {
		name   string
		breaks func(*memory.Store, *apiv2.Config)
		lists  func(*testing.T, *apiv2.Service, scene) error
	}{
		{
			name:   "projects",
			breaks: func(st *memory.Store, c *apiv2.Config) { c.Projects = brokenProjects{st.Projects(), errLeak} },
			lists: func(t *testing.T, svc *apiv2.Service, s scene) error {
				_, err := svc.ListProjects(context.Background(), apiv2.ListProjectsRequest{})
				return err
			},
		},
		{
			name: "environments",
			breaks: func(st *memory.Store, c *apiv2.Config) {
				c.Environments = brokenEnvironments{st.Environments(), errLeak}
			},
			lists: func(t *testing.T, svc *apiv2.Service, s scene) error {
				_, err := svc.ListEnvironments(context.Background(), apiv2.ListEnvironmentsRequest{
					ProjectID: string(s.environment.ProjectID),
				})
				return err
			},
		},
		{
			name:   "releases",
			breaks: func(st *memory.Store, c *apiv2.Config) { c.Releases = brokenReleases{st.Releases(), errLeak} },
			lists: func(t *testing.T, svc *apiv2.Service, s scene) error {
				_, err := svc.ListReleases(context.Background(), apiv2.ListReleasesRequest{
					ProjectID: string(s.environment.ProjectID),
				})
				return err
			},
		},
		{
			name:   "artifacts",
			breaks: func(st *memory.Store, c *apiv2.Config) { c.Artifacts = brokenArtifacts{st.Artifacts(), errLeak} },
			lists: func(t *testing.T, svc *apiv2.Service, s scene) error {
				_, err := svc.ListArtifacts(context.Background(), apiv2.ListArtifactsRequest{
					ProjectID: string(s.environment.ProjectID),
				})
				return err
			},
		},
		{
			name: "deployments",
			breaks: func(st *memory.Store, c *apiv2.Config) {
				c.Deployments = brokenDeployments{st.Deployments(), errLeak}
			},
			lists: func(t *testing.T, svc *apiv2.Service, s scene) error {
				_, err := svc.ListDeployments(context.Background(), apiv2.ListDeploymentsRequest{
					EnvironmentID: string(s.environment.ID),
				})
				return err
			},
		},
		{
			name:   "deployment events",
			breaks: func(st *memory.Store, c *apiv2.Config) { c.Events = brokenEvents{st.Events(), errLeak} },
			lists: func(t *testing.T, svc *apiv2.Service, s scene) error {
				d := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)
				_, err := svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
					DeploymentID: string(d.ID),
				})
				return err
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := setup(t)
			s := w.scene(t)
			cfg := config(w.store, w.clock, stubDeployer{})
			tc.breaks(w.store, &cfg)
			svc, err := apiv2.New(cfg)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			err = tc.lists(t, svc, s)

			var e *apierr.Error
			if !errors.As(err, &e) {
				t.Fatalf("error %v is not an *apierr.Error", err)
			}
			if e.Code != apierr.Internal {
				t.Errorf("code = %s, want %s", e.Code, apierr.Internal)
			}
			if containsWord(e.Message, "10.0.0.7") {
				t.Errorf("message %q hands the caller the estate's network layout", e.Message)
			}
			if !errors.Is(err, errLeak) {
				t.Error("the cause did not survive for the log; an operator has nothing to debug with")
			}
		})
	}
}

// TestACursorThisAPIDidNotIssueIsRefusedByEveryList keeps the lists from
// drifting apart on what a forged cursor means.
func TestACursorThisAPIDidNotIssueIsRefusedByEveryList(t *testing.T) {
	forged := page.Request{Cursor: "not-a-cursor"}
	for _, tc := range []struct {
		name string
		list func(*testing.T, scene) error
	}{
		{"projects", func(t *testing.T, s scene) error {
			_, err := s.svc.ListProjects(context.Background(), apiv2.ListProjectsRequest{Page: forged})
			return err
		}},
		{"environments", func(t *testing.T, s scene) error {
			_, err := s.svc.ListEnvironments(context.Background(), apiv2.ListEnvironmentsRequest{
				ProjectID: string(s.environment.ProjectID), Page: forged,
			})
			return err
		}},
		{"releases", func(t *testing.T, s scene) error {
			_, err := s.svc.ListReleases(context.Background(), apiv2.ListReleasesRequest{
				ProjectID: string(s.environment.ProjectID), Page: forged,
			})
			return err
		}},
		{"artifacts", func(t *testing.T, s scene) error {
			_, err := s.svc.ListArtifacts(context.Background(), apiv2.ListArtifactsRequest{
				ProjectID: string(s.environment.ProjectID), Page: forged,
			})
			return err
		}},
		{"deployments", func(t *testing.T, s scene) error {
			_, err := s.svc.ListDeployments(context.Background(), apiv2.ListDeploymentsRequest{
				EnvironmentID: string(s.environment.ID), Page: forged,
			})
			return err
		}},
		{"deployment events", func(t *testing.T, s scene) error {
			d := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)
			_, err := s.svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
				DeploymentID: string(d.ID), Page: forged,
			})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := setup(t).scene(t)

			if got := codeOf(t, tc.list(t, s)); got != apierr.InvalidArgument {
				t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
			}
		})
	}
}

// TestAServiceNamesEveryDependencyItWasNotGiven holds New to naming the gap
// rather than failing later as a nil dereference in a handler.
func TestAServiceNamesEveryDependencyItWasNotGiven(t *testing.T) {
	for _, tc := range []struct {
		missing string
		drop    func(*apiv2.Config)
	}{
		{"project", func(c *apiv2.Config) { c.Projects = nil }},
		{"environment", func(c *apiv2.Config) { c.Environments = nil }},
		{"release", func(c *apiv2.Config) { c.Releases = nil }},
		{"artifact", func(c *apiv2.Config) { c.Artifacts = nil }},
		{"deployment", func(c *apiv2.Config) { c.Deployments = nil }},
		{"plan", func(c *apiv2.Config) { c.Plans = nil }},
		{"event", func(c *apiv2.Config) { c.Events = nil }},
		{"deployer", func(c *apiv2.Config) { c.Deployer = nil }},
		{"founder", func(c *apiv2.Config) { c.Founder = nil }},
		{"binder", func(c *apiv2.Config) { c.Binder = nil }},
		{"idempotency", func(c *apiv2.Config) { c.Keys = nil }},
		{"clock", func(c *apiv2.Config) { c.Clock = nil }},
	} {
		t.Run(tc.missing, func(t *testing.T) {
			st := memory.New()
			cfg := config(st, &movableClock{now: at}, stubDeployer{})
			tc.drop(&cfg)

			_, err := apiv2.New(cfg)

			if err == nil {
				t.Fatalf("a service with no %s repository was constructed", tc.missing)
			}
			if !containsWord(err.Error(), tc.missing) {
				t.Errorf("error %q does not name the missing dependency", err)
			}
		})
	}
}

func containsWord(s, w string) bool {
	for i := 0; i+len(w) <= len(s); i++ {
		if s[i:i+len(w)] == w {
			return true
		}
	}
	return false
}

// lister reads one page of whatever a row of the paging table arranged.
type lister func(page.Request) (ids []string, next string, err error)

// TestEveryListPagesAndResumesWhereItStopped states the paging contract once.
// A cursor keyed on the wrong column, or on something that is not unique,
// surfaces here as a repeated or a missing row — rather than in whichever list
// happened to be given a test of its own.
func TestEveryListPagesAndResumesWhereItStopped(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// arrange brings the list to four rows, counting whatever the scene
		// already created, and returns a way to read a page of them.
		arrange func(*testing.T, scene) lister
	}{
		{"projects", func(t *testing.T, s scene) lister {
			for _, n := range []string{"billing", "search", "ledger"} {
				s.project(t, n)
			}
			return func(req page.Request) ([]string, string, error) {
				got, err := s.svc.ListProjects(ctx, apiv2.ListProjectsRequest{Page: req})
				return identifiers(got.Projects, func(p apiv2.Project) string { return p.ID }), got.Next, err
			}
		}},
		{"environments", func(t *testing.T, s scene) lister {
			for _, n := range []string{"staging", "canary", "sandbox"} {
				s.world.environment(t, s.environment.ProjectID, environment.Environment{Name: n})
			}
			return func(req page.Request) ([]string, string, error) {
				got, err := s.svc.ListEnvironments(ctx, apiv2.ListEnvironmentsRequest{
					ProjectID: string(s.environment.ProjectID), Page: req,
				})
				return identifiers(got.Environments, func(e apiv2.Environment) string { return e.ID }), got.Next, err
			}
		}},
		{"releases", func(t *testing.T, s scene) lister {
			for _, v := range []string{"2.1.0", "2.2.0", "2.3.0"} {
				s.world.release(t, s.environment.ProjectID, v, map[string]identity.ArtifactID{
					"app": s.artifact(t, s.environment.ProjectID, v).ID,
				})
			}
			return func(req page.Request) ([]string, string, error) {
				got, err := s.svc.ListReleases(ctx, apiv2.ListReleasesRequest{
					ProjectID: string(s.environment.ProjectID), Page: req,
				})
				return identifiers(got.Releases, func(r apiv2.Release) string { return r.ID }), got.Next, err
			}
		}},
		{"artifacts", func(t *testing.T, s scene) lister {
			for _, tag := range []string{"sidecar", "migrator", "worker"} {
				s.artifact(t, s.environment.ProjectID, tag)
			}
			return func(req page.Request) ([]string, string, error) {
				got, err := s.svc.ListArtifacts(ctx, apiv2.ListArtifactsRequest{
					ProjectID: string(s.environment.ProjectID), Page: req,
				})
				return identifiers(got.Artifacts, func(a apiv2.Artifact) string { return a.ID }), got.Next, err
			}
		}},
		{"deployments", func(t *testing.T, s scene) lister {
			p := s.plan(t, oneApply(), allowed())
			for range 4 {
				s.deployment(t, p.ID)
			}
			return func(req page.Request) ([]string, string, error) {
				got, err := s.svc.ListDeployments(ctx, apiv2.ListDeploymentsRequest{
					EnvironmentID: string(s.environment.ID), Page: req,
				})
				return identifiers(got.Deployments, func(d apiv2.Deployment) string { return d.ID }), got.Next, err
			}
		}},
		{"deployment events", func(t *testing.T, s scene) lister {
			d := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)
			for range 4 {
				s.record(t, d, event.DeploymentOperationCompleted, `{}`)
			}
			return func(req page.Request) ([]string, string, error) {
				got, err := s.svc.DeploymentEvents(ctx, apiv2.DeploymentEventsRequest{
					DeploymentID: string(d.ID), Page: req,
				})
				return identifiers(got.Events, func(e apiv2.Event) string { return e.ID }), got.Next, err
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list := tc.arrange(t, setup(t).scene(t))

			first, next, err := list(page.Request{Size: 3})
			if err != nil {
				t.Fatalf("first page: %v", err)
			}
			if len(first) != 3 {
				t.Fatalf("got %d rows on a page of 3, want 3", len(first))
			}
			if next == "" {
				t.Fatal("no cursor; the caller cannot reach the fourth row")
			}

			second, next, err := list(page.Request{Size: 3, Cursor: next})
			if err != nil {
				t.Fatalf("second page: %v", err)
			}
			if len(second) != 1 {
				t.Fatalf("got %d rows on the second page, want the remaining 1", len(second))
			}
			if next != "" {
				t.Error("a cursor past the end; a client would ask for a page that is never coming")
			}
			seen := map[string]bool{}
			for _, id := range append(first, second...) {
				if seen[id] {
					t.Errorf("row %q came back on both pages", id)
				}
				seen[id] = true
			}
		})
	}
}

func identifiers[T any](rows []T, id func(T) string) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, id(r))
	}
	return out
}
