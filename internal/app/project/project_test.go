package project_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	app "go.klarlabs.de/rollops/internal/app/project"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
	"go.klarlabs.de/rollops/internal/store/memory"
)

var start = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// actor carries a claim that must not reach storage. A project record is not
// attributed, so the only place a credential could land here is the timeline
// entry — which is the one place nothing may rewrite it out of (INV-012,
// INV-013).
func actor() identity.Principal {
	return identity.Principal{
		ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada",
		Claims: map[string]string{"token": "s3cret", "email": "ada@example.com"},
	}
}

type harness struct {
	store   *memory.Store
	clock   fixedClock
	service *app.Service
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	clk := fixedClock{now: start}
	store := memory.New()

	svc, err := app.New(app.Config{
		Transactor: store,
		Projects:   store.Projects(),
		Events:     store.Events(),
		Clock:      clk,
		IDs:        identity.NewSequenceGenerator(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &harness{store: store, clock: clk, service: svc}
}

// creating is the command every test starts from.
func (h *harness) creating() app.CreateCommand {
	return app.CreateCommand{
		Name:        "checkout",
		Description: "the thing that takes the money",
		Labels:      map[string]string{"tier": "critical"},
		Actor:       actor(),
	}
}

func (h *harness) create(t *testing.T, cmd app.CreateCommand) project.Project {
	t.Helper()
	p, err := h.service.Create(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return p
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

// ── creating a project ───────────────────────────────────────────────────────

func TestCreatingAProjectMakesItAddressable(t *testing.T) {
	h := newHarness(t)
	cmd := h.creating()

	got, err := h.service.Create(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID == "" {
		t.Fatal("no project id; nothing could be filed under it")
	}
	if got.Name != cmd.Name {
		t.Errorf("name = %q, want %q", got.Name, cmd.Name)
	}
	if !got.CreatedAt.Equal(start) {
		t.Errorf("created at %s, want %s", got.CreatedAt, start)
	}

	stored, err := h.store.Projects().Get(context.Background(), got.ID)
	if err != nil {
		t.Fatalf("Projects.Get: %v", err)
	}
	if stored.Name != cmd.Name {
		t.Errorf("persisted name = %q, want %q", stored.Name, cmd.Name)
	}
	if stored.Description != cmd.Description {
		t.Errorf("persisted description = %q, want %q", stored.Description, cmd.Description)
	}
	if stored.Labels["tier"] != "critical" {
		t.Errorf("persisted labels = %v, want the tier label", stored.Labels)
	}
}

// A project is how everything else is addressed, so it has to be findable by
// the name a person will type rather than only by the id a machine minted.
func TestACreatedProjectIsFoundByName(t *testing.T) {
	h := newHarness(t)
	want := h.create(t, h.creating())

	got, err := h.store.Projects().GetByName(context.Background(), "checkout")
	if err != nil {
		t.Fatalf("Projects.GetByName: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("name resolved to %q, want %q", got.ID, want.ID)
	}
}

func TestCreatingAProjectRecordsItOnTheTimeline(t *testing.T) {
	h := newHarness(t)
	p := h.create(t, h.creating())

	e := find(t, h.timeline(t), event.ProjectCreated)
	if e.AggregateType != event.AggregateProject {
		t.Errorf("filed against %s, want %s", e.AggregateType, event.AggregateProject)
	}
	if e.AggregateID != string(p.ID) {
		t.Errorf("filed against %q, want %q", e.AggregateID, p.ID)
	}
	if !strings.Contains(string(e.Payload), `"name":"checkout"`) {
		t.Errorf("payload %s does not name the project", e.Payload)
	}
}

// The name a project was created under stays true after a rename, where the
// current name would not. The description is left out for the reason the
// release payload leaves labels out: it is prose somebody edits, and a log
// nothing may rewrite should not hold a claim that stops being true.
func TestTheProjectEventCarriesTheNameAndNotTheProse(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.creating())

	e := find(t, h.timeline(t), event.ProjectCreated)
	if strings.Contains(string(e.Payload), "takes the money") {
		t.Errorf("payload %s copies the description", e.Payload)
	}
}

func TestTheProjectAuthorIsRecordedWithoutTheirCredentials(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.creating())

	e := find(t, h.timeline(t), event.ProjectCreated)
	if e.Principal.ID != actor().ID {
		t.Errorf("principal = %q, want %q", e.Principal.ID, actor().ID)
	}
	// The key survives — that a principal presented a token is part of what
	// policy saw. What must not survive is the token itself.
	if e.Principal.Claims["token"] == actor().Claims["token"] {
		t.Error("the author's token was written to the timeline")
	}
}

// A name is how a project is addressed on every surface, so two of them is an
// ambiguity rather than something to reconcile. Unlike an artifact, whose
// content identifies it, a second create under a taken name is a different
// project asking for a name that is spoken for.
func TestASecondProjectCannotTakeANameAlreadyUsed(t *testing.T) {
	h := newHarness(t)
	h.create(t, h.creating())

	_, err := h.service.Create(context.Background(), h.creating())
	if !errors.Is(err, port.ErrAlreadyExists) {
		t.Fatalf("err = %v, want ErrAlreadyExists", err)
	}

	all, err := h.store.Projects().List(context.Background())
	if err != nil {
		t.Fatalf("Projects.List: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("%d projects stored, want 1", len(all))
	}
}

// Nothing is recorded for a project that does not exist. A timeline naming a
// project nothing can be filed under sends whoever reads it looking for a
// record that was never written.
func TestARefusedNameRecordsNothing(t *testing.T) {
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
		{"no name", func(c *app.CreateCommand) { c.Name = "" }, project.ErrInvalidName},
		{"not an alias", func(c *app.CreateCommand) { c.Name = "Check Out!" }, project.ErrInvalidName},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t)
			cmd := h.creating()
			tt.edit(&cmd)

			_, err := h.service.Create(context.Background(), cmd)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
			// Also marked as the caller's, so that a transport answers with the
			// code that sends them back to their request rather than one that has
			// them reporting an outage.
			if !errors.Is(err, app.ErrRejected) {
				t.Errorf("err = %v, want it marked ErrRejected", err)
			}
			if got := len(h.timeline(t)); got != 0 {
				t.Errorf("%d events for a project that was never made, want 0", got)
			}
		})
	}
}

// ── construction ─────────────────────────────────────────────────────────────

func TestAServiceMissingADependencyIsRefusedAndSaysWhich(t *testing.T) {
	_, err := app.New(app.Config{})
	if !errors.Is(err, app.ErrIncomplete) {
		t.Fatalf("err = %v, want ErrIncomplete", err)
	}
	for _, want := range []string{"transactor", "projects repository", "events log", "clock", "ids generator"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err %q does not name the missing %s", err, want)
		}
	}
}
