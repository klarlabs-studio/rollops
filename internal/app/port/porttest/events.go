package porttest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// eventFixture builds envelopes against one deterministic world. It keeps its
// own generator so that an aggregate id and the event filed against it come
// from the same sequence and a failure names both.
type eventFixture struct {
	gen   identity.Generator
	clock identity.Clock
	by    identity.Principal
	dep   string
	rel   string
}

func newEventFixture(t *testing.T) *eventFixture {
	t.Helper()
	f := newFixture()
	dep, err := identity.NewDeploymentID(f.gen)
	if err != nil {
		t.Fatalf("deployment id: %v", err)
	}
	rel, err := identity.NewReleaseID(f.gen)
	if err != nil {
		t.Fatalf("release id: %v", err)
	}
	return &eventFixture{
		gen:   f.gen,
		clock: f.clock,
		by:    identity.Principal{ID: "ada", Type: identity.PrincipalHuman},
		dep:   string(dep),
		rel:   string(rel),
	}
}

func (f *eventFixture) event(t *testing.T, e event.Event) event.Event {
	t.Helper()
	if e.AggregateType == "" {
		e.AggregateType = event.AggregateDeployment
		e.AggregateID = f.dep
	}
	if e.Type == "" {
		e.Type = event.DeploymentStarted
	}
	got, err := event.New(f.gen, f.clock, f.by, e)
	if err != nil {
		t.Fatalf("building event: %v", err)
	}
	return got
}

// appended writes es inside one transaction and returns them with the
// sequences the log assigned. Every append in the suite goes through here
// because an append outside a transaction is the one thing that must fail.
func (f *eventFixture) appended(t *testing.T, r Repositories, es ...event.Event) []event.Event {
	t.Helper()
	out := make([]event.Event, 0, len(es))
	err := r.Tx.WithinTransaction(context.Background(), func(ctx context.Context) error {
		for _, e := range es {
			got, err := r.Events.Append(ctx, e)
			if err != nil {
				return err
			}
			out = append(out, got)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("appending: %v", err)
	}
	return out
}

func sequences(es []event.Event) []uint64 {
	out := make([]uint64, len(es))
	for i, e := range es {
		out[i] = e.Sequence
	}
	return out
}

func ids(es []event.Event) []identity.EventID {
	out := make([]identity.EventID, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func sameIDs(got []event.Event, want []identity.EventID) bool {
	if len(got) != len(want) {
		return false
	}
	for i, e := range got {
		if e.ID != want[i] {
			return false
		}
	}
	return true
}

func runEvents(t *testing.T, newRepos Factory) {
	ctx := context.Background()

	t.Run("an append outside a transaction fails", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		_, err := r.Events.Append(ctx, f.event(t, event.Event{}))
		if !errors.Is(err, port.ErrNoTransaction) {
			t.Fatalf("Append = %v, want ErrNoTransaction", err)
		}
		// And it must not have written anything on the way to failing.
		got, err := r.Events.Timeline(ctx, port.Page{})
		if err != nil {
			t.Fatalf("Timeline: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("the failed append left %d events behind", len(got))
		}
	})

	t.Run("the log assigns a gapless sequence from one", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		got := f.appended(t, r,
			f.event(t, event.Event{Type: event.DeploymentPlanCreated}),
			f.event(t, event.Event{Type: event.DeploymentStarted}),
			f.event(t, event.Event{Type: event.DeploymentSucceeded}),
		)
		want := []uint64{1, 2, 3}
		if seq := sequences(got); !equalUint64(seq, want) {
			t.Errorf("sequences = %v, want %v", seq, want)
		}
	})

	// The sequence is what a cursor pages by, so a hole in it is a page that
	// silently reports fewer events than happened. A rolled-back append must
	// therefore not consume a position.
	t.Run("a rolled-back append leaves no gap", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		sentinel := errors.New("rolled back on purpose")

		err := r.Tx.WithinTransaction(ctx, func(ctx context.Context) error {
			if _, err := r.Events.Append(ctx, f.event(t, event.Event{})); err != nil {
				return err
			}
			return sentinel
		})
		if !errors.Is(err, sentinel) {
			t.Fatalf("WithinTransaction = %v, want the callback's error", err)
		}

		got := f.appended(t, r, f.event(t, event.Event{}))
		if got[0].Sequence != 1 {
			t.Errorf("Sequence = %d, want 1 — the rolled-back append kept its position", got[0].Sequence)
		}
		all, err := r.Events.Timeline(ctx, port.Page{})
		if err != nil {
			t.Fatalf("Timeline: %v", err)
		}
		if len(all) != 1 {
			t.Errorf("Timeline returned %d events, want only the committed one", len(all))
		}
	})

	t.Run("an event survives the round trip whole", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		root := f.event(t, event.Event{Type: event.DeploymentPlanCreated})
		want := f.event(t, event.Event{
			Type:          event.DeploymentOperationFailed,
			Version:       3,
			CorrelationID: root.ID,
			CausationID:   root.ID,
			Payload:       json.RawMessage(`{"operation":"apply","attempt":2}`),
			Metadata:      map[string]string{"source": "webhook"},
		})
		appended := f.appended(t, r, root, want)

		got, err := r.Events.ForAggregate(ctx, event.AggregateDeployment, f.dep, port.Page{})
		if err != nil {
			t.Fatalf("ForAggregate: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %d events, want 2", len(got))
		}
		assertEvent(t, got[1], appended[1])
	})

	t.Run("a payload the writer omitted stays omitted", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		f.appended(t, r, f.event(t, event.Event{Type: event.DeploymentSucceeded}))

		got, err := r.Events.Timeline(ctx, port.Page{})
		if err != nil {
			t.Fatalf("Timeline: %v", err)
		}
		if len(got[0].Payload) != 0 {
			t.Errorf("Payload = %s, want nothing invented", got[0].Payload)
		}
	})

	// §16.4: consumers MUST tolerate unknown event types. A store that
	// validated on read would turn a downgrade — or the older half of a rolling
	// upgrade — into a log nobody can open. The envelope is built directly here
	// rather than through event.New precisely because New would refuse it.
	t.Run("a type this binary does not know survives a round trip", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		id, err := identity.NewEventID(f.gen)
		if err != nil {
			t.Fatalf("event id: %v", err)
		}
		future := event.Event{
			ID:            id,
			Type:          "deployment.teleported",
			Version:       9,
			AggregateType: event.AggregateDeployment,
			AggregateID:   f.dep,
			Time:          f.clock.Now(),
			Principal:     f.by,
			CorrelationID: id,
		}
		f.appended(t, r, future)

		got, err := r.Events.Timeline(ctx, port.Page{})
		if err != nil {
			t.Fatalf("Timeline: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d events, want 1", len(got))
		}
		if got[0].Type != "deployment.teleported" {
			t.Errorf("Type = %q, want the unknown type carried through", got[0].Type)
		}
	})

	// INV-012 again, at the boundary this time. event.New already redacts, but
	// the store is the last thing between a credential and a row nothing may
	// ever rewrite, so it does not trust its caller to have used the
	// constructor.
	t.Run("a credential does not reach the log", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		id, err := identity.NewEventID(f.gen)
		if err != nil {
			t.Fatalf("event id: %v", err)
		}
		f.appended(t, r, event.Event{
			ID:            id,
			Type:          event.DeploymentStarted,
			Version:       1,
			AggregateType: event.AggregateDeployment,
			AggregateID:   f.dep,
			Time:          f.clock.Now(),
			Principal: identity.Principal{
				ID:     "ci",
				Type:   identity.PrincipalService,
				Claims: map[string]string{"token": "ghp_real", "email": "ci@example.com"},
			},
			CorrelationID: id,
			Metadata:      map[string]string{"x-authorization": "Bearer abc", "source": "webhook"},
		})

		got, err := r.Events.Timeline(ctx, port.Page{})
		if err != nil {
			t.Fatalf("Timeline: %v", err)
		}
		if got[0].Principal.Claims["token"] == "ghp_real" {
			t.Errorf("a credential reached the log: %v", got[0].Principal.Claims)
		}
		if got[0].Principal.Claims["email"] != "ci@example.com" {
			t.Errorf("attribution was lost with the credential: %v", got[0].Principal.Claims)
		}
		if got[0].Metadata["x-authorization"] == "Bearer abc" {
			t.Errorf("a forwarded header reached the log: %v", got[0].Metadata)
		}
		if got[0].Metadata["source"] != "webhook" {
			t.Errorf("benign metadata was lost: %v", got[0].Metadata)
		}
	})

	t.Run("an aggregate sees only its own events", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		appended := f.appended(t, r,
			f.event(t, event.Event{Type: event.DeploymentStarted}),
			f.event(t, event.Event{
				Type:          event.ReleaseCreated,
				AggregateType: event.AggregateRelease,
				AggregateID:   f.rel,
			}),
			f.event(t, event.Event{Type: event.DeploymentSucceeded}),
		)

		got, err := r.Events.ForAggregate(ctx, event.AggregateDeployment, f.dep, port.Page{})
		if err != nil {
			t.Fatalf("ForAggregate: %v", err)
		}
		if want := []identity.EventID{appended[0].ID, appended[2].ID}; !sameIDs(got, want) {
			t.Errorf("ForAggregate = %v, want %v", ids(got), want)
		}

		all, err := r.Events.Timeline(ctx, port.Page{})
		if err != nil {
			t.Fatalf("Timeline: %v", err)
		}
		if !sameIDs(all, ids(appended)) {
			t.Errorf("Timeline = %v, want every event in order %v", ids(all), ids(appended))
		}
	})

	// The same aggregate id filed under two kinds is not a case that should
	// arise, but the filter has to test both columns or a timeline could show
	// an event belonging to something else that happens to share an id.
	t.Run("the aggregate filter tests the kind as well as the id", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		f.appended(t, r, f.event(t, event.Event{Type: event.DeploymentStarted}))

		got, err := r.Events.ForAggregate(ctx, event.AggregateRelease, f.dep, port.Page{})
		if err != nil {
			t.Fatalf("ForAggregate: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("got %d events, want none — the id belongs to a deployment", len(got))
		}
	})

	t.Run("one intent reads back as one story", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		root := f.event(t, event.Event{Type: event.DeploymentPlanCreated})
		appended := f.appended(t, r,
			root,
			f.event(t, event.Event{
				Type:          event.ReleaseCreated,
				AggregateType: event.AggregateRelease,
				AggregateID:   f.rel,
				CorrelationID: root.ID,
				CausationID:   root.ID,
			}),
			// A different intent, on the aggregate the first one touched.
			f.event(t, event.Event{Type: event.DeploymentFailed}),
		)

		got, err := r.Events.ForCorrelation(ctx, root.ID, port.Page{})
		if err != nil {
			t.Fatalf("ForCorrelation: %v", err)
		}
		want := []identity.EventID{appended[0].ID, appended[1].ID}
		if !sameIDs(got, want) {
			t.Errorf("ForCorrelation = %v, want %v — one intent across two aggregates", ids(got), want)
		}
	})

	t.Run("reads page by sequence", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		// One aggregate, one intent, so all three reads see the same three
		// events and page identically.
		root := f.event(t, event.Event{Type: event.DeploymentPlanCreated})
		appended := f.appended(t, r,
			root,
			f.event(t, event.Event{Type: event.DeploymentStarted, CorrelationID: root.ID, CausationID: root.ID}),
			f.event(t, event.Event{Type: event.DeploymentSucceeded, CorrelationID: root.ID, CausationID: root.ID}),
		)

		reads := map[string]func(context.Context, port.Page) ([]event.Event, error){
			"Timeline": r.Events.Timeline,
			"ForAggregate": func(ctx context.Context, p port.Page) ([]event.Event, error) {
				return r.Events.ForAggregate(ctx, event.AggregateDeployment, f.dep, p)
			},
			"ForCorrelation": func(ctx context.Context, p port.Page) ([]event.Event, error) {
				return r.Events.ForCorrelation(ctx, appended[0].CorrelationID, p)
			},
		}
		for name, read := range reads {
			t.Run(name, func(t *testing.T) {
				first, err := read(ctx, port.Page{Limit: 2})
				if err != nil {
					t.Fatalf("first page: %v", err)
				}
				if want := ids(appended)[:2]; !sameIDs(first, want) {
					t.Fatalf("first page = %v, want %v", ids(first), want)
				}

				next, err := read(ctx, port.Page{After: first[len(first)-1].Sequence, Limit: 2})
				if err != nil {
					t.Fatalf("second page: %v", err)
				}
				if want := ids(appended)[2:]; !sameIDs(next, want) {
					t.Errorf("second page = %v, want %v", ids(next), want)
				}

				past, err := read(ctx, port.Page{After: appended[2].Sequence})
				if err != nil {
					t.Fatalf("page past the end: %v", err)
				}
				if len(past) != 0 {
					t.Errorf("a cursor past the end returned %d events", len(past))
				}
			})
		}
	})

	// An unset limit must not mean "no rows" and must not mean "everything".
	// Both stores read the bound from port.Page so the two cannot disagree.
	t.Run("an unset limit is a default, not nothing", func(t *testing.T) {
		f, r := newEventFixture(t), newRepos(t)
		f.appended(t, r, f.event(t, event.Event{}))

		for name, p := range map[string]port.Page{
			"zero":     {Limit: 0},
			"negative": {Limit: -1},
			"over max": {Limit: port.MaxPageSize + 1},
		} {
			t.Run(name, func(t *testing.T) {
				got, err := r.Events.Timeline(ctx, p)
				if err != nil {
					t.Fatalf("Timeline: %v", err)
				}
				if len(got) != 1 {
					t.Errorf("got %d events, want 1", len(got))
				}
			})
		}
	})
}

func assertEvent(t *testing.T, got, want event.Event) {
	t.Helper()
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if got.Type != want.Type {
		t.Errorf("Type = %q, want %q", got.Type, want.Type)
	}
	if got.Version != want.Version {
		t.Errorf("Version = %d, want %d", got.Version, want.Version)
	}
	if got.AggregateType != want.AggregateType || got.AggregateID != want.AggregateID {
		t.Errorf("aggregate = %s/%s, want %s/%s",
			got.AggregateType, got.AggregateID, want.AggregateType, want.AggregateID)
	}
	if got.Sequence != want.Sequence {
		t.Errorf("Sequence = %d, want %d", got.Sequence, want.Sequence)
	}
	if !got.Time.Equal(want.Time) {
		t.Errorf("Time = %v, want %v", got.Time, want.Time)
	}
	if got.Principal.ID != want.Principal.ID || got.Principal.Type != want.Principal.Type {
		t.Errorf("Principal = %+v, want %+v", got.Principal, want.Principal)
	}
	if got.CorrelationID != want.CorrelationID {
		t.Errorf("CorrelationID = %q, want %q", got.CorrelationID, want.CorrelationID)
	}
	if got.CausationID != want.CausationID {
		t.Errorf("CausationID = %q, want %q", got.CausationID, want.CausationID)
	}
	if string(got.Payload) != string(want.Payload) {
		t.Errorf("Payload = %s, want %s", got.Payload, want.Payload)
	}
	if len(got.Metadata) != len(want.Metadata) {
		t.Errorf("Metadata = %v, want %v", got.Metadata, want.Metadata)
	}
	for k, v := range want.Metadata {
		if got.Metadata[k] != v {
			t.Errorf("Metadata[%q] = %q, want %q", k, got.Metadata[k], v)
		}
	}
}

func equalUint64(got, want []uint64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
