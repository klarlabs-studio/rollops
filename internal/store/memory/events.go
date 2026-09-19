package memory

import (
	"context"
	"maps"
	"slices"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

type events struct{ s *Store }

// Append refuses to write outside a transaction (ADR-0003). The other
// repositories fall back to their own unit of work when there is none; the log
// must not, because an event whose aggregate write was rolled back is a lie in
// the timeline and the fallback is exactly what would produce one.
func (r events) Append(ctx context.Context, e event.Event) (event.Event, error) {
	st, open := txFrom(ctx)
	if !open {
		return event.Event{}, port.ErrNoTransaction
	}
	e.Sequence = uint64(len(st.events)) + 1
	e.Principal = e.Principal.Redacted()
	e.Metadata = identity.RedactSecrets(e.Metadata)
	st.events = append(st.events, copyEvent(e))
	return copyEvent(e), nil
}

func (r events) ForAggregate(
	ctx context.Context, a event.AggregateType, id string, p port.Page,
) ([]event.Event, error) {
	return r.filter(ctx, p, func(e event.Event) bool {
		return e.AggregateType == a && e.AggregateID == id
	})
}

func (r events) Timeline(ctx context.Context, p port.Page) ([]event.Event, error) {
	return r.filter(ctx, p, func(event.Event) bool { return true })
}

func (r events) ForCorrelation(
	ctx context.Context, c identity.EventID, p port.Page,
) ([]event.Event, error) {
	return r.filter(ctx, p, func(e event.Event) bool { return e.CorrelationID == c })
}

// filter walks the log in order and takes at most one page of what keep
// accepts. The slice is already ordered by sequence, so there is nothing to
// sort — that is the reason the log is a slice.
func (r events) filter(
	ctx context.Context, p port.Page, keep func(event.Event) bool,
) ([]event.Event, error) {
	p = p.Normalized()
	out := []event.Event{}
	err := r.s.read(ctx, func(st *state) error {
		for _, e := range st.events {
			if e.Sequence <= p.After || !keep(e) {
				continue
			}
			out = append(out, copyEvent(e))
			if len(out) == p.Limit {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// copyEvent deep-copies the three fields that are references. An event is
// immutable once appended, so a caller holding a slice into the log would be
// the only way to break that.
func copyEvent(e event.Event) event.Event {
	e.Payload = slices.Clone(e.Payload)
	e.Metadata = maps.Clone(e.Metadata)
	e.Principal.Claims = maps.Clone(e.Principal.Claims)
	return e
}
