package apiv2

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// Event is one durable record of something that happened.
//
// Payload is passed through as the log holds it. Nothing here could redact it —
// it is opaque JSON by design, which is most of why adding a field to it is not
// a migration — so INV-012 is met where the event is built, and event.New
// refuses to append one that is unattributed. Reading it back is what the log
// is for; a timeline that withheld the payload would answer "something
// happened" and nothing else.
type Event struct {
	ID   string
	Type string

	// Version is the schema version of Payload, per type. A reader that does
	// not recognise the pair tolerates it rather than failing (§16.4).
	Version int

	AggregateType string
	AggregateID   string
	Sequence      uint64
	Time          time.Time
	Principal     Principal
	CorrelationID string
	CausationID   string
	Payload       json.RawMessage
	Metadata      map[string]string
}

// DeploymentEventsRequest names the deployment whose log to read.
type DeploymentEventsRequest struct {
	DeploymentID string
	Page         page.Request
}

// DeploymentEventsResponse is a page of one deployment's events.
type DeploymentEventsResponse struct {
	Events []Event
	Next   string
}

// DeploymentEvents returns a page of one deployment's events, oldest first.
//
// It reads the deployment first so that an id nobody deployed is a not-found
// rather than an empty page. The log cannot tell the two apart — it holds no
// rows for either — and a client polling a mistyped id would wait forever for
// a deployment that was never going to report anything.
//
// Unlike the other lists this one pages in the repository rather than over a
// slice it already has: an event log is the one thing here that grows without
// bound, and the cursor is the event's sequence, which is gapless and
// monotonic (ADR-0003).
func (s *Service) DeploymentEvents(
	ctx context.Context, req DeploymentEventsRequest,
) (DeploymentEventsResponse, error) {
	id, err := identity.ParseDeploymentID(req.DeploymentID)
	if err != nil {
		return DeploymentEventsResponse{}, badArgument("apiv2: deployment id: %w", err)
	}
	if _, err := s.deployments.Get(ctx, id); err != nil {
		return DeploymentEventsResponse{}, failure("apiv2: deployment %s: %w", id, err)
	}
	after, err := sequenceCursor(req.Page)
	if err != nil {
		return DeploymentEventsResponse{}, badArgument("apiv2: deployment %s: events: %w", id, err)
	}
	// Fetch asks for one more row than the page holds, which is the only way to
	// tell a log that ended from one that ended on the boundary.
	stored, err := s.events.ForAggregate(ctx, event.AggregateDeployment, string(id), port.Page{
		After: after,
		Limit: req.Page.Fetch(),
	})
	if err != nil {
		return DeploymentEventsResponse{}, failure("apiv2: deployment %s: events: %w", id, err)
	}
	p := page.Trim(stored, req.Page, sequenceKey)
	return DeploymentEventsResponse{Events: mapped(p.Items, viewEvent), Next: p.Next}, nil
}

// sequenceCursor decodes the cursor into the sequence to resume after. A
// cursor this API issued for some other list decodes cleanly and then means
// nothing here, so it is refused rather than read as a position.
func sequenceCursor(req page.Request) (uint64, error) {
	start, err := req.Start()
	if err != nil {
		return 0, err
	}
	if start == "" {
		return 0, nil
	}
	after, err := strconv.ParseUint(start, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: it names no position in the log", page.ErrBadCursor)
	}
	return after, nil
}

func sequenceKey(e event.Event) string { return strconv.FormatUint(e.Sequence, 10) }

func viewEvent(e event.Event) Event {
	return Event{
		ID:            string(e.ID),
		Type:          string(e.Type),
		Version:       e.Version,
		AggregateType: string(e.AggregateType),
		AggregateID:   e.AggregateID,
		Sequence:      e.Sequence,
		Time:          e.Time,
		Principal:     viewPrincipal(e.Principal),
		CorrelationID: string(e.CorrelationID),
		CausationID:   string(e.CausationID),
		Payload:       e.Payload,
		Metadata:      copyMap(e.Metadata),
	}
}
