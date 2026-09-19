package event

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

func fixtures(t *testing.T) (identity.Generator, identity.Clock, identity.Principal) {
	t.Helper()
	return identity.NewSequenceGenerator(), identity.NewFixedClock(at), identity.Principal{
		ID:   "ada",
		Type: identity.PrincipalHuman,
	}
}

func deploymentID(t *testing.T, g identity.Generator) string {
	t.Helper()
	id, err := identity.NewDeploymentID(g)
	if err != nil {
		t.Fatalf("deployment id: %v", err)
	}
	return string(id)
}

func TestANewEventIsIdentifiedStampedAndAttributed(t *testing.T) {
	g, c, by := fixtures(t)
	agg := deploymentID(t, g)

	got, err := New(g, c, by, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
		Payload:       json.RawMessage(`{"environment":"production"}`),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := identity.ParseEventID(string(got.ID)); err != nil {
		t.Errorf("event id %q: %v", got.ID, err)
	}
	if !got.Time.Equal(at) {
		t.Errorf("Time = %v, want %v", got.Time, at)
	}
	if got.Principal.ID != "ada" {
		t.Errorf("Principal = %+v, want ada", got.Principal)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1 by default", got.Version)
	}
}

// Sequence is the log's total order and only the appender can know it
// (ADR-0003). A constructor that accepted one would let a caller claim a
// position in a log it has not been written to yet.
func TestANewEventHasNoSequenceYet(t *testing.T) {
	g, c, by := fixtures(t)
	agg := deploymentID(t, g)

	got, err := New(g, c, by, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.Sequence != 0 {
		t.Errorf("Sequence = %d, want 0 until the appender assigns one", got.Sequence)
	}

	_, err = New(g, c, by, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
		Sequence:      7,
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("a caller-chosen sequence was accepted: %v", err)
	}
}

// §16.4 requires a correlation id on every event so one user intent can be
// followed across plan, apply and verify. A field that may be left empty is one
// that will be, so an event that names no chain starts its own.
func TestAnEventWithoutACorrelationStartsItsOwnChain(t *testing.T) {
	g, c, by := fixtures(t)
	agg := deploymentID(t, g)

	first, err := New(g, c, by, Event{
		Type:          DeploymentPlanCreated,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if first.CorrelationID != first.ID {
		t.Errorf("CorrelationID = %q, want the event's own id %q", first.CorrelationID, first.ID)
	}
	if first.CausationID != "" {
		t.Errorf("CausationID = %q, want empty for a root event", first.CausationID)
	}

	next, err := New(g, c, by, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
		CorrelationID: first.CorrelationID,
		CausationID:   first.ID,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if next.CorrelationID != first.ID {
		t.Errorf("CorrelationID = %q, want the chain it was given %q", next.CorrelationID, first.ID)
	}
	if next.CausationID != first.ID {
		t.Errorf("CausationID = %q, want %q", next.CausationID, first.ID)
	}
}

func TestCorrelationAndCausationMustNameEvents(t *testing.T) {
	g, c, by := fixtures(t)
	agg := deploymentID(t, g)

	for _, tc := range []struct {
		name string
		e    Event
	}{
		{"correlation", Event{CorrelationID: identity.EventID(agg)}},
		{"causation", Event{CausationID: identity.EventID(agg)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.e
			e.Type = DeploymentStarted
			e.AggregateType = AggregateDeployment
			e.AggregateID = agg
			if _, err := New(g, c, by, e); !errors.Is(err, ErrInvalidEvent) {
				t.Errorf("a deployment id was accepted as an event reference: %v", err)
			}
		})
	}
}

// A timeline is read by filtering on the aggregate, so an event filed against
// the wrong kind of id is invisible where it matters and misleading where it
// lands. identity already distinguishes misrouted from malformed; this is the
// one place that distinction earns its keep.
func TestAnEventCannotBeFiledAgainstAnotherKindOfAggregate(t *testing.T) {
	g, c, by := fixtures(t)
	rel, err := identity.NewReleaseID(g)
	if err != nil {
		t.Fatalf("release id: %v", err)
	}

	_, err = New(g, c, by, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   string(rel),
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("a release id was filed as a deployment: %v", err)
	}
	if !errors.Is(err, identity.ErrWrongKind) {
		t.Errorf("want the misrouting reported as such, got %v", err)
	}
}

func TestEveryAggregateTypeAcceptsItsOwnID(t *testing.T) {
	g, c, by := fixtures(t)

	for _, tc := range []struct {
		agg AggregateType
		new func(identity.Generator) (string, error)
	}{
		{AggregateProject, func(g identity.Generator) (string, error) {
			id, err := identity.NewProjectID(g)
			return string(id), err
		}},
		{AggregateEnvironment, func(g identity.Generator) (string, error) {
			id, err := identity.NewEnvironmentID(g)
			return string(id), err
		}},
		{AggregateArtifact, func(g identity.Generator) (string, error) {
			id, err := identity.NewArtifactID(g)
			return string(id), err
		}},
		{AggregateRelease, func(g identity.Generator) (string, error) {
			id, err := identity.NewReleaseID(g)
			return string(id), err
		}},
		{AggregateDeployment, func(g identity.Generator) (string, error) {
			id, err := identity.NewDeploymentID(g)
			return string(id), err
		}},
		{AggregatePlan, func(g identity.Generator) (string, error) {
			id, err := identity.NewPlanID(g)
			return string(id), err
		}},
		{AggregateVerificationRun, func(g identity.Generator) (string, error) {
			id, err := identity.NewVerificationRunID(g)
			return string(id), err
		}},
		{AggregatePipelineRun, func(g identity.Generator) (string, error) {
			id, err := identity.NewPipelineRunID(g)
			return string(id), err
		}},
	} {
		t.Run(string(tc.agg), func(t *testing.T) {
			id, err := tc.new(g)
			if err != nil {
				t.Fatalf("id: %v", err)
			}
			if _, err := New(g, c, by, Event{
				Type:          PolicyEvaluated,
				AggregateType: tc.agg,
				AggregateID:   id,
			}); err != nil {
				t.Errorf("New: %v", err)
			}
		})
	}
}

// The log is append-only, so a payload that is not JSON can never be repaired
// — every later read of that row fails on a value nothing may rewrite.
func TestAPayloadThatIsNotJSONIsRefused(t *testing.T) {
	g, c, by := fixtures(t)
	agg := deploymentID(t, g)

	_, err := New(g, c, by, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
		Payload:       json.RawMessage(`{"environment":`),
	})
	if !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("truncated JSON was accepted: %v", err)
	}
}

func TestAnAbsentPayloadIsFine(t *testing.T) {
	g, c, by := fixtures(t)
	agg := deploymentID(t, g)

	got, err := New(g, c, by, Event{
		Type:          DeploymentSucceeded,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.Payload != nil {
		t.Errorf("Payload = %s, want nil rather than an invented body", got.Payload)
	}
}

// INV-012. An event is written once and read on every timeline afterwards, so
// a credential that reached the envelope could never be recalled. Attribution
// is why the principal is there at all, so what is not a credential survives.
func TestTheEnvelopeIsRedactedBeforeItExists(t *testing.T) {
	g, c, _ := fixtures(t)
	agg := deploymentID(t, g)

	got, err := New(g, c, identity.Principal{
		ID:     "ci",
		Type:   identity.PrincipalService,
		Claims: map[string]string{"token": "ghp_real", "email": "ci@example.com"},
	}, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
		Metadata:      map[string]string{"x-authorization": "Bearer abc", "source": "webhook"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got.Principal.Claims["token"] == "ghp_real" {
		t.Errorf("a credential survived onto the envelope: %v", got.Principal.Claims)
	}
	if got.Principal.Claims["email"] != "ci@example.com" {
		t.Errorf("attribution was lost with the credential: %v", got.Principal.Claims)
	}
	if got.Metadata["x-authorization"] == "Bearer abc" {
		t.Errorf("a forwarded header survived onto the envelope: %v", got.Metadata)
	}
	if got.Metadata["source"] != "webhook" {
		t.Errorf("benign metadata was lost: %v", got.Metadata)
	}
}

func TestAnEventCannotBeUnattributed(t *testing.T) {
	g, c, _ := fixtures(t)
	agg := deploymentID(t, g)

	_, err := New(g, c, identity.Principal{}, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   agg,
	})
	if !errors.Is(err, identity.ErrInvalidPrincipal) {
		t.Errorf("an anonymous event was accepted: %v", err)
	}
}

func TestNewRejectsAnIncompleteEnvelope(t *testing.T) {
	g, c, by := fixtures(t)
	agg := deploymentID(t, g)

	for _, tc := range []struct {
		name string
		e    Event
	}{
		{"no type", Event{AggregateType: AggregateDeployment, AggregateID: agg}},
		{"unknown type", Event{Type: "deployment.reticulated", AggregateType: AggregateDeployment, AggregateID: agg}},
		{"no aggregate type", Event{Type: DeploymentStarted, AggregateID: agg}},
		{"unknown aggregate type", Event{Type: DeploymentStarted, AggregateType: "sprocket", AggregateID: agg}},
		{"no aggregate id", Event{Type: DeploymentStarted, AggregateType: AggregateDeployment}},
		{"malformed aggregate id", Event{Type: DeploymentStarted, AggregateType: AggregateDeployment, AggregateID: "dep_nope"}},
		{"negative version", Event{Type: DeploymentStarted, AggregateType: AggregateDeployment, AggregateID: agg, Version: -1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(g, c, by, tc.e); !errors.Is(err, ErrInvalidEvent) {
				t.Errorf("accepted %s: %v", tc.name, err)
			}
		})
	}
}

// §16.4: consumers MUST tolerate unknown event types. A binary reading a log
// written by a newer one — after a downgrade, or while a rolling upgrade is
// half done — must be able to hand back the row rather than fail on it. That is
// only possible if an unrecognised type is representable at all, which is why
// Type is a string and not a closed enum.
func TestAnUnknownTypeIsRepresentableEvenThoughItCannotBeWritten(t *testing.T) {
	future := Type("deployment.teleported")
	if future.Valid() {
		t.Fatalf("%q should not be in this binary's vocabulary", future)
	}
	if got := string(future); got != "deployment.teleported" {
		t.Errorf("an unknown type did not survive being carried: %q", got)
	}
}

func TestTheVocabularyMatchesTheSpec(t *testing.T) {
	// §16.3. Listed here as text so that adding a constant without adding it to
	// the canon, or the reverse, is a failing test rather than a silent drift.
	want := strings.Fields(`
		project.created environment.created artifact.registered release.created
		deployment.plan.created deployment.queued
		deployment.approval.requested deployment.approved
		deployment.approval.rejected
		deployment.started deployment.operation.started
		deployment.operation.completed deployment.operation.failed
		deployment.verification.started deployment.verification.completed
		deployment.paused deployment.promotion.started deployment.promoted
		deployment.succeeded deployment.failed
		rollback.started rollback.completed rollback.failed
		drift.detected reconcile.started reconcile.completed reconcile.failed
		policy.evaluated security.access_denied
	`)
	if len(Types()) != len(want) {
		t.Fatalf("vocabulary has %d types, spec §16.3 lists %d", len(Types()), len(want))
	}
	for i, w := range want {
		if got := Types()[i]; string(got) != w {
			t.Errorf("Types()[%d] = %q, want %q", i, got, w)
		}
		if !Type(w).Valid() {
			t.Errorf("%q is in the spec but not valid", w)
		}
	}
}

func TestNewRefusesABrokenGenerator(t *testing.T) {
	_, c, by := fixtures(t)
	g := identity.GeneratorFunc(func() (string, error) { return "", errors.New("exhausted") })

	if _, err := New(g, c, by, Event{
		Type:          DeploymentStarted,
		AggregateType: AggregateDeployment,
		AggregateID:   "dep_00000000-0000-7000-8000-000000000001",
	}); err == nil {
		t.Error("an event was created without an identifier")
	}
}
