package apiv2_test

import (
	"context"
	"encoding/json"
	"testing"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/event"
)

// record appends one event against a deployment, the way the apply engine will.
func (s scene) record(t *testing.T, d deployment.Deployment, typ event.Type, payload string) event.Event {
	t.Helper()
	e, err := event.New(s.ids, s.clock, planner, event.Event{
		Type:          typ,
		AggregateType: event.AggregateDeployment,
		AggregateID:   string(d.ID),
		Payload:       json.RawMessage(payload),
		Metadata:      map[string]string{"region": "eu-central-1", "api_token": "s3cret"},
	})
	if err != nil {
		t.Fatalf("event.New: %v", err)
	}
	var stored event.Event
	err = s.store.WithinTransaction(context.Background(), func(ctx context.Context) error {
		var err error
		stored, err = s.store.Events().Append(ctx, e)
		return err
	})
	if err != nil {
		t.Fatalf("Events.Append: %v", err)
	}
	return stored
}

func TestADeploymentsEventsComeBackOldestFirst(t *testing.T) {
	s := setup(t).scene(t)
	d := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)
	s.record(t, d, event.DeploymentQueued, `{"strategy":"rolling"}`)
	s.record(t, d, event.DeploymentStarted, `{}`)
	s.record(t, d, event.DeploymentSucceeded, `{}`)

	got, err := s.svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
		DeploymentID: string(d.ID),
	})
	if err != nil {
		t.Fatalf("DeploymentEvents: %v", err)
	}
	if len(got.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(got.Events))
	}
	want := []event.Type{event.DeploymentQueued, event.DeploymentStarted, event.DeploymentSucceeded}
	for i, w := range want {
		if got.Events[i].Type != string(w) {
			t.Errorf("event %d = %q, want %q", i, got.Events[i].Type, w)
		}
	}
	if got.Events[0].Sequence >= got.Events[1].Sequence {
		t.Error("sequences do not increase; a cursor over them would loop or skip")
	}
}

func TestAnEventCarriesThePayloadThatSaysWhatHappened(t *testing.T) {
	s := setup(t).scene(t)
	d := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)
	s.record(t, d, event.DeploymentQueued, `{"strategy":"rolling"}`)

	got, err := s.svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
		DeploymentID: string(d.ID),
	})
	if err != nil {
		t.Fatalf("DeploymentEvents: %v", err)
	}
	var payload struct {
		Strategy string `json:"strategy"`
	}
	if err := json.Unmarshal(got.Events[0].Payload, &payload); err != nil {
		t.Fatalf("payload %s is not json: %v", got.Events[0].Payload, err)
	}
	if payload.Strategy != "rolling" {
		t.Errorf("payload = %s", got.Events[0].Payload)
	}
	if got.Events[0].AggregateID != string(d.ID) || got.Events[0].AggregateType != string(event.AggregateDeployment) {
		t.Errorf("event is filed against %s %q", got.Events[0].AggregateType, got.Events[0].AggregateID)
	}
	if got.Events[0].Version == 0 {
		t.Error("version is zero; a reader cannot tell which shape the payload is in")
	}
}

func TestAnEventTiesItselfToTheIntentThatCausedIt(t *testing.T) {
	s := setup(t).scene(t)
	d := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)
	stored := s.record(t, d, event.DeploymentQueued, `{}`)

	got, err := s.svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
		DeploymentID: string(d.ID),
	})
	if err != nil {
		t.Fatalf("DeploymentEvents: %v", err)
	}
	if got.Events[0].ID != string(stored.ID) {
		t.Errorf("id = %q, want %q", got.Events[0].ID, stored.ID)
	}
	if got.Events[0].CorrelationID == "" {
		t.Error("no correlation id; the event cannot be joined to the rest of its story")
	}
	if got.Events[0].Principal.ID != planner.ID {
		t.Errorf("principal = %+v; an unattributed event breaks INV-005", got.Events[0].Principal)
	}
}

func TestAnEventsMetadataArrivesWithoutTheSecretsItWasWrittenWith(t *testing.T) {
	s := setup(t).scene(t)
	d := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)
	s.record(t, d, event.DeploymentQueued, `{}`)

	got, err := s.svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
		DeploymentID: string(d.ID),
	})
	if err != nil {
		t.Fatalf("DeploymentEvents: %v", err)
	}
	if got.Events[0].Metadata["region"] != "eu-central-1" {
		t.Errorf("metadata = %v; the ordinary keys did not survive", got.Events[0].Metadata)
	}
	if containsWord(got.Events[0].Metadata["api_token"], "s3cret") {
		t.Errorf("metadata = %v; the token reached the wire", got.Events[0].Metadata)
	}
}

// TestEventsOfAnotherDeploymentAreNotInThisOnesTimeline is the whole reason the
// read filters by aggregate rather than paging the global log.
func TestEventsOfAnotherDeploymentAreNotInThisOnesTimeline(t *testing.T) {
	s := setup(t).scene(t)
	p := s.plan(t, oneApply(), allowed())
	mine := s.deployment(t, p.ID)
	theirs := s.deployment(t, p.ID)
	s.record(t, theirs, event.DeploymentFailed, `{}`)
	s.record(t, mine, event.DeploymentSucceeded, `{}`)

	got, err := s.svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
		DeploymentID: string(mine.ID),
	})
	if err != nil {
		t.Fatalf("DeploymentEvents: %v", err)
	}
	if len(got.Events) != 1 || got.Events[0].Type != string(event.DeploymentSucceeded) {
		t.Fatalf("events = %+v", got.Events)
	}
}

// TestTheEventsOfADeploymentNobodyStartedAreNotAnEmptyPage keeps a mistyped id
// from looking like a deployment that has not done anything yet.
func TestTheEventsOfADeploymentNobodyStartedAreNotAnEmptyPage(t *testing.T) {
	s := setup(t).scene(t)
	absent := s.deployment(t, s.plan(t, oneApply(), allowed()).ID)

	_, err := setup(t).svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
		DeploymentID: string(absent.ID),
	})

	if got := codeOf(t, err); got != apierr.NotFound {
		t.Errorf("code = %s, want %s", got, apierr.NotFound)
	}
}

func TestAnEventReadNeedsADeploymentToReadTheEventsOf(t *testing.T) {
	w := setup(t)

	_, err := w.svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}

// TestACursorThatDecodesToSomethingOtherThanASequenceIsRefused covers the one
// cursor this read can be handed that the other lists cannot: ours decodes to a
// number, and a well-formed cursor from a different list does not.
func TestACursorThatDecodesToSomethingOtherThanASequenceIsRefused(t *testing.T) {
	s := setup(t).scene(t)
	p := s.plan(t, oneApply(), allowed())
	d := s.deployment(t, p.ID)
	s.deployment(t, p.ID)
	s.record(t, d, event.DeploymentQueued, `{}`)

	list, err := s.svc.ListDeployments(context.Background(), apiv2.ListDeploymentsRequest{
		EnvironmentID: string(s.environment.ID),
		Page:          page.Request{Size: 1},
	})
	if err != nil {
		t.Fatalf("ListDeployments: %v", err)
	}

	_, err = s.svc.DeploymentEvents(context.Background(), apiv2.DeploymentEventsRequest{
		DeploymentID: string(d.ID),
		Page:         page.Request{Cursor: list.Next},
	})

	if got := codeOf(t, err); got != apierr.InvalidArgument {
		t.Errorf("code = %s, want %s", got, apierr.InvalidArgument)
	}
}
