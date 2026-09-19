package apiv2_test

import (
	"context"
	"errors"
	"testing"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/store/memory"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

type world struct {
	svc   *apiv2.Service
	store *memory.Store
	ids   identity.Generator
	clock identity.Clock
}

func setup(t *testing.T) *world {
	t.Helper()
	store := memory.New()
	svc, err := apiv2.New(apiv2.Config{
		Projects:     store.Projects(),
		Environments: store.Environments(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &world{
		svc:   svc,
		store: store,
		ids:   identity.NewGenerator(),
		clock: identity.ClockFunc(func() time.Time { return at }),
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
	return env
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

func TestProjectsComeBackAPageAtATime(t *testing.T) {
	w := setup(t)
	for _, n := range []string{"alpha", "bravo", "charlie", "delta"} {
		w.project(t, n)
	}

	got, err := w.svc.ListProjects(context.Background(), apiv2.ListProjectsRequest{
		Page: page.Request{Size: 2},
	})
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(got.Projects) != 2 {
		t.Fatalf("got %d projects, want 2", len(got.Projects))
	}
	if got.Next == "" {
		t.Fatal("no cursor; the caller cannot reach the other two")
	}
}

func TestTheProjectListResumesWhereItStopped(t *testing.T) {
	w := setup(t)
	for _, n := range []string{"alpha", "bravo", "charlie", "delta"} {
		w.project(t, n)
	}
	ctx := context.Background()

	var seen []string
	req := apiv2.ListProjectsRequest{Page: page.Request{Size: 2}}
	for {
		got, err := w.svc.ListProjects(ctx, req)
		if err != nil {
			t.Fatalf("ListProjects: %v", err)
		}
		for _, p := range got.Projects {
			seen = append(seen, p.Name)
		}
		if got.Next == "" {
			break
		}
		req.Page = page.Request{Cursor: got.Next, Size: 2}
	}

	if len(seen) != 4 {
		t.Fatalf("saw %v, want all four exactly once", seen)
	}
}

func TestACursorThisAPIDidNotIssueIsRefused(t *testing.T) {
	w := setup(t)
	w.project(t, "checkout")

	_, err := w.svc.ListProjects(context.Background(), apiv2.ListProjectsRequest{
		Page: page.Request{Cursor: "not-a-cursor"},
	})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
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

func TestAServiceSaysWhichDependencyItWasNotGiven(t *testing.T) {
	_, err := apiv2.New(apiv2.Config{Projects: memory.New().Projects()})

	if err == nil {
		t.Fatal("a service missing a repository was constructed; the failure would surface as a nil dereference on the first call")
	}
	if !containsWord(err.Error(), "environment") {
		t.Errorf("error %q does not name the missing dependency", err)
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

func TestAServiceWithNoRepositoriesAtAllNamesTheFirstOneMissing(t *testing.T) {
	_, err := apiv2.New(apiv2.Config{})

	if err == nil {
		t.Fatal("an empty config was accepted")
	}
	if !containsWord(err.Error(), "project") {
		t.Errorf("error %q does not name the missing dependency", err)
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

func TestACursorThisAPIDidNotIssueIsRefusedWhenListingEnvironments(t *testing.T) {
	w := setup(t)
	p := w.project(t, "checkout")
	w.environment(t, p.ID, environment.Environment{Name: "production"})

	_, err := w.svc.ListEnvironments(context.Background(), apiv2.ListEnvironmentsRequest{
		ProjectID: string(p.ID),
		Page:      page.Request{Cursor: "not-a-cursor"},
	})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

// brokenProjects and brokenEnvironments fail their list read with something
// nothing has classified — the shape a driver error arrives in.
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

func TestAStorageFailureDoesNotTellTheCallerAboutTheStorage(t *testing.T) {
	leak := errors.New("dial tcp 10.0.0.7:5432: connect: connection refused")
	store := memory.New()
	svc, err := apiv2.New(apiv2.Config{
		Projects:     brokenProjects{ProjectRepository: store.Projects(), err: leak},
		Environments: store.Environments(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = svc.ListProjects(context.Background(), apiv2.ListProjectsRequest{})

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
	if !errors.Is(err, leak) {
		t.Error("the cause did not survive for the log; an operator has nothing to debug with")
	}
}

func TestAStorageFailureListingEnvironmentsIsAlsoInternal(t *testing.T) {
	leak := errors.New("dial tcp 10.0.0.7:5432: connect: connection refused")
	store := memory.New()
	svc, err := apiv2.New(apiv2.Config{
		Projects:     store.Projects(),
		Environments: brokenEnvironments{EnvironmentRepository: store.Environments(), err: leak},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	w := &world{svc: svc, store: store, ids: identity.NewGenerator(), clock: identity.ClockFunc(func() time.Time { return at })}
	p := w.project(t, "checkout")

	_, err = svc.ListEnvironments(context.Background(), apiv2.ListEnvironmentsRequest{ProjectID: string(p.ID)})

	if got := codeOf(t, err); got != apierr.Internal {
		t.Errorf("code = %s, want %s", got, apierr.Internal)
	}
}
