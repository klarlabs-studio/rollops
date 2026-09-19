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
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/release"
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
		Releases:     store.Releases(),
		Artifacts:    store.Artifacts(),
		Deployments:  store.Deployments(),
		Plans:        store.Plans(),
		Events:       store.Events(),
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
			cfg := apiv2.Config{
				Projects:     w.store.Projects(),
				Environments: w.store.Environments(),
				Releases:     w.store.Releases(),
				Artifacts:    w.store.Artifacts(),
				Deployments:  w.store.Deployments(),
				Plans:        w.store.Plans(),
				Events:       w.store.Events(),
			}
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
	} {
		t.Run(tc.missing, func(t *testing.T) {
			st := memory.New()
			cfg := apiv2.Config{
				Projects:     st.Projects(),
				Environments: st.Environments(),
				Releases:     st.Releases(),
				Artifacts:    st.Artifacts(),
				Deployments:  st.Deployments(),
				Plans:        st.Plans(),
				Events:       st.Events(),
			}
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
