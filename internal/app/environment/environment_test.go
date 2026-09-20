package environment_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	app "go.klarlabs.de/rollops/internal/app/environment"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/name"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/domain/value"
	"go.klarlabs.de/rollops/internal/store/memory"
)

var start = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func actor() identity.Principal {
	return identity.Principal{
		ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada",
		Claims: map[string]string{"token": "s3cret", "email": "ada@example.com"},
	}
}

type harness struct {
	store   *memory.Store
	gen     identity.Generator
	clock   fixedClock
	service *app.Service
	project project.Project
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	gen := identity.NewSequenceGenerator()
	clk := fixedClock{now: start}
	store := memory.New()

	proj, err := project.New(gen, clk, project.Project{Name: "checkout"})
	if err != nil {
		t.Fatalf("build project: %v", err)
	}
	if _, err := store.Projects().Create(context.Background(), proj); err != nil {
		t.Fatalf("store project: %v", err)
	}

	svc, err := app.New(app.Config{
		Transactor:   store,
		Projects:     store.Projects(),
		Environments: store.Environments(),
		Events:       store.Events(),
		Clock:        clk,
		IDs:          gen,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{store: store, gen: gen, clock: clk, service: svc, project: proj}
}

// creating is the command every test starts from: one environment wired to one
// cluster, configured with a literal and a secret. The secret is the point —
// it must reach the environment record and nothing else.
func (h *harness) creating() app.CreateCommand {
	return app.CreateCommand{
		ProjectID: h.project.ID,
		Name:      "production",
		Kind:      environment.KindProduction,
		Targets: []environment.TargetBinding{{
			Name:   "api",
			Driver: "kubernetes",
			Config: map[string]value.Ref{
				"namespace":  value.Literal("payments"),
				"kubeconfig": value.Secret("prod/kubeconfig"),
			},
		}},
		Policies: []environment.PolicyBinding{{
			Name: "two-eyes",
			Ref:  "policies/two-eyes.cel",
			Mode: environment.PolicyEnforce,
		}},
		Variables: map[string]value.Ref{"DATABASE_URL": value.Secret("prod/db")},
		Labels:    map[string]string{"tier": "critical"},
		Actor:     actor(),
	}
}

func (h *harness) create(t *testing.T, cmd app.CreateCommand) environment.Environment {
	t.Helper()
	e, err := h.service.Create(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return e
}

func (h *harness) timeline(t *testing.T) []event.Event {
	t.Helper()
	es, err := h.store.Events().Timeline(context.Background(), port.Page{})
	if err != nil {
		t.Fatalf("read timeline: %v", err)
	}
	return es
}

func find(t *testing.T, es []event.Event, typ event.Type) event.Event {
	t.Helper()
	for _, e := range es {
		if e.Type == typ {
			return e
		}
	}
	t.Fatalf("no %s on a timeline of %d events", typ, len(es))
	return event.Event{}
}

// ── creating an environment ──────────────────────────────────────────────────

func TestCreatingAnEnvironmentMakesItDeployableTo(t *testing.T) {
	h := newHarness(t)

	got, err := h.service.Create(context.Background(), h.creating())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID == "" {
		t.Fatal("no environment id; nothing could be planned against it")
	}
	if !got.CanDeploy() {
		t.Error("the environment has no target; a plan against it would find nowhere to land")
	}

	stored, err := h.store.Environments().Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}
	if stored.Name != "production" {
		t.Errorf("persisted name = %q, want production", stored.Name)
	}
	if stored.Kind != environment.KindProduction {
		t.Errorf("persisted kind = %q, want production", stored.Kind)
	}
	if got, want := stored.Variables["DATABASE_URL"].SecretName(), "prod/db"; got != want {
		t.Errorf("persisted variable names secret %q, want %q", got, want)
	}
}

func TestACreatedEnvironmentReportsTheRevisionItWasStoredAt(t *testing.T) {
	h := newHarness(t)
	got := h.create(t, h.creating())

	stored, err := h.store.Environments().Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Environments.Get: %v", err)
	}
	if got.Revision != stored.Revision {
		t.Errorf("Create returned revision %d, stored at %d", got.Revision, stored.Revision)
	}
}

// A name is how an environment is asked for, and it is unique within a project
// rather than across them: two projects may each have a production.
func TestTwoProjectsMayEachHaveAProduction(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.creating())

	other, err := project.New(h.gen, h.clock, project.Project{Name: "billing"})
	if err != nil {
		t.Fatalf("build project: %v", err)
	}
	if _, err := h.store.Projects().Create(context.Background(), other); err != nil {
		t.Fatalf("store project: %v", err)
	}
	cmd := h.creating()
	cmd.ProjectID = other.ID

	if _, err := h.service.Create(context.Background(), cmd); err != nil {
		t.Fatalf("Create in a second project: %v", err)
	}
}

func TestASecondEnvironmentCannotTakeANameAlreadyUsedInTheProject(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.creating())

	_, err := h.service.Create(context.Background(), h.creating())

	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}
}

// An environment is filed under a project, so a project that does not exist is
// a missing resource rather than a malformed request.
func TestAnEnvironmentForAProjectNobodyCreatedIsNotFound(t *testing.T) {
	h := newHarness(t)
	cmd := h.creating()
	absent, err := identity.NewProjectID(h.gen)
	if err != nil {
		t.Fatalf("NewProjectID: %v", err)
	}
	cmd.ProjectID = absent

	_, err = h.service.Create(context.Background(), cmd)

	if !errors.Is(err, port.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCreatingAnEnvironmentRecordsItOnTheTimeline(t *testing.T) {
	h := newHarness(t)
	e := h.create(t, h.creating())

	rec := find(t, h.timeline(t), event.EnvironmentCreated)
	if rec.AggregateType != event.AggregateEnvironment {
		t.Errorf("filed against %s, want %s", rec.AggregateType, event.AggregateEnvironment)
	}
	if rec.AggregateID != string(e.ID) {
		t.Errorf("filed against %q, want %q", rec.AggregateID, e.ID)
	}
	body := string(rec.Payload)
	for _, want := range []string{`"name":"production"`, `"kind":"production"`, `"driver":"kubernetes"`} {
		if !strings.Contains(body, want) {
			t.Errorf("payload %s does not contain %s", body, want)
		}
	}
}

// The values never leave a value.Ref, so the question is the keys — and they
// are absent too. A log is the one record that cannot be redacted afterwards
// (INV-013), and a list of configuration keys is an inventory of how the estate
// holds its credentials. The environment record keeps them for whoever asks for
// that environment.
func TestTheEnvironmentEventNamesNeitherSecretsNorTheKeysHoldingThem(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.creating())

	body := string(find(t, h.timeline(t), event.EnvironmentCreated).Payload)
	for _, leaked := range []string{"prod/kubeconfig", "prod/db", "kubeconfig", "DATABASE_URL", "payments"} {
		if strings.Contains(body, leaked) {
			t.Errorf("payload %s names %q", body, leaked)
		}
	}
}

func TestTheEnvironmentAuthorIsRecordedWithoutTheirCredentials(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.creating())

	rec := find(t, h.timeline(t), event.EnvironmentCreated)
	if rec.Principal.ID != actor().ID {
		t.Errorf("principal = %q, want %q", rec.Principal.ID, actor().ID)
	}
	if rec.Principal.Claims["token"] == actor().Claims["token"] {
		t.Error("the author's token was written to the timeline")
	}
}

// Nothing is recorded for an environment that does not exist.
func TestARefusedEnvironmentRecordsNothing(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.creating())
	before := len(h.timeline(t))

	if _, err := h.service.Create(context.Background(), h.creating()); err == nil {
		t.Fatal("Create: want an error for the taken name")
	}

	if got := len(h.timeline(t)); got != before {
		t.Errorf("%d events after the refusal, want the %d from before", got, before)
	}
}

func TestAMalformedCommandIsTheCallersMistake(t *testing.T) {
	tests := []struct {
		name string
		edit func(*app.CreateCommand)
		want error
	}{
		{"no name", func(c *app.CreateCommand) { c.Name = "" }, name.ErrInvalid},
		{"unknown kind", func(c *app.CreateCommand) { c.Kind = "prodction" }, nil},
		{"two targets with one name", func(c *app.CreateCommand) {
			c.Targets = append(c.Targets, c.Targets[0])
		}, environment.ErrDuplicateTarget},
		{"target with no driver", func(c *app.CreateCommand) { c.Targets[0].Driver = "" }, nil},
		{"policy with no reference", func(c *app.CreateCommand) { c.Policies[0].Ref = "" }, nil},
		{"negative ttl", func(c *app.CreateCommand) {
			c.Lifecycle = environment.Lifecycle{TTL: -time.Hour}
		}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			cmd := h.creating()
			tt.edit(&cmd)

			_, err := h.service.Create(context.Background(), cmd)
			if err == nil {
				t.Fatal("Create: want a refusal")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			// Marked as the caller's, so that a transport sends them back to
			// their request rather than having them report an outage.
			if !errors.Is(err, app.ErrRejected) {
				t.Errorf("err = %v, want it marked ErrRejected", err)
			}
			if got := len(h.timeline(t)); got != 0 {
				t.Errorf("%d events for an environment that was never made, want 0", got)
			}
		})
	}
}

// An environment may be declared before the infrastructure it names exists.
// Refusing one with no target would make the record wait on the cluster, when
// the point of the record is to describe what the cluster should become.
func TestAnEnvironmentWithNoTargetIsAllowed(t *testing.T) {
	h := newHarness(t)
	cmd := h.creating()
	cmd.Targets = nil

	got, err := h.service.Create(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.CanDeploy() {
		t.Error("an environment with no targets reports it can deploy")
	}
}

// ── construction ─────────────────────────────────────────────────────────────

func TestAServiceMissingADependencyIsRefusedAndSaysWhich(t *testing.T) {
	_, err := app.New(app.Config{})
	if !errors.Is(err, app.ErrIncomplete) {
		t.Fatalf("err = %v, want ErrIncomplete", err)
	}
	for _, want := range []string{
		"transactor", "projects repository", "environments repository",
		"events log", "clock", "ids generator",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q does not name the missing %s", err, want)
		}
	}
}
