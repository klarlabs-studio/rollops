package sqlite

import (
	"context"
	"database/sql"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// Events returns the domain event log over this store.
func (s *Store) Events() port.EventLog { return eventRepo{s} }

type eventRepo struct{ s *Store }

// Append refuses to write outside a transaction (ADR-0003). The other
// repositories open their own unit of work when there is none; the log must
// not, because an event whose aggregate write was rolled back is a lie in the
// timeline and that fallback is exactly what would produce one.
//
// The sequence comes from the insert rather than from a count, so it is the
// database's autoincrement and not a number this process guessed — two writers
// racing cannot arrive at the same position.
func (r eventRepo) Append(ctx context.Context, e event.Event) (event.Event, error) {
	tx, open := txFrom(ctx)
	if !open {
		return event.Event{}, port.ErrNoTransaction
	}
	// Redacted here as well as in event.New, because this is the last thing
	// between a credential and a row nothing may ever rewrite (INV-012).
	e.Principal = e.Principal.Redacted()
	e.Metadata = identity.RedactSecrets(e.Metadata)

	principal, err := encodePrincipal(e.Principal)
	if err != nil {
		return event.Event{}, err
	}
	metadata, err := encodeStringMap(e.Metadata)
	if err != nil {
		return event.Event{}, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO events (id, type, version, aggregate_type, aggregate_id, at,
		                    principal, correlation_id, causation_id, payload, metadata)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		string(e.ID), string(e.Type), e.Version, string(e.AggregateType), e.AggregateID,
		encodeTime(e.Time), principal, string(e.CorrelationID), string(e.CausationID),
		string(e.Payload), metadata,
	)
	if err != nil {
		return event.Event{}, wrap("append event", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return event.Event{}, wrap("append event sequence", err)
	}
	e.Sequence = uint64(seq)
	return e, nil
}

func (r eventRepo) ForAggregate(
	ctx context.Context, a event.AggregateType, id string, p port.Page,
) ([]event.Event, error) {
	return r.page(ctx, p, `AND aggregate_type = ? AND aggregate_id = ?`, string(a), id)
}

func (r eventRepo) Timeline(ctx context.Context, p port.Page) ([]event.Event, error) {
	return r.page(ctx, p, ``)
}

func (r eventRepo) ForCorrelation(
	ctx context.Context, c identity.EventID, p port.Page,
) ([]event.Event, error) {
	return r.page(ctx, p, `AND correlation_id = ?`, string(c))
}

const eventColumns = `SELECT sequence, id, type, version, aggregate_type, aggregate_id, at,
                             principal, correlation_id, causation_id, payload, metadata
                      FROM events`

// page runs one read: the cursor first, then the caller's filter, then the
// order and the bound. The three reads differ only in that filter, so they
// cannot page differently from one another.
func (r eventRepo) page(
	ctx context.Context, p port.Page, filter string, filterArgs ...any,
) ([]event.Event, error) {
	p = p.Normalized()
	args := make([]any, 0, len(filterArgs)+2)
	args = append(args, p.After)
	args = append(args, filterArgs...)
	args = append(args, p.Limit)

	rows, err := r.s.conn(ctx).QueryContext(ctx,
		eventColumns+` WHERE sequence > ? `+filter+` ORDER BY sequence LIMIT ?`, args...)
	if err != nil {
		return nil, wrap("read events", err)
	}
	defer func() { _ = rows.Close() }()

	out := []event.Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("read events", err)
	}
	return out, nil
}

// scanEvent does not check that the type is one this binary knows. Spec 16.4
// requires tolerance of unknown event types, and refusing a newer row would
// turn a downgrade into data loss.
func scanEvent(rows *sql.Rows) (event.Event, error) {
	var (
		e                      event.Event
		seq                    int64
		id, typ, aggregate     string
		at, principal          string
		correlation, causation string
		payload, metadata      string
	)
	if err := rows.Scan(&seq, &id, &typ, &e.Version, &aggregate, &e.AggregateID, &at,
		&principal, &correlation, &causation, &payload, &metadata); err != nil {
		return event.Event{}, wrap("scan event", err)
	}
	e.Sequence = uint64(seq)
	e.ID = identity.EventID(id)
	e.Type = event.Type(typ)
	e.AggregateType = event.AggregateType(aggregate)
	e.CorrelationID = identity.EventID(correlation)
	e.CausationID = identity.EventID(causation)
	e.Payload = blobBytes(payload)

	var err error
	if e.Time, err = decodeTime(at); err != nil {
		return event.Event{}, err
	}
	if e.Principal, err = decodePrincipal(principal); err != nil {
		return event.Event{}, err
	}
	if e.Metadata, err = decodeStringMap(metadata); err != nil {
		return event.Event{}, err
	}
	return e, nil
}
