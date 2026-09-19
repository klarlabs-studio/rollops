-- 0014_event_log: what happened, in the order it happened — the domain event
-- log (spec 16.2, ADR-0005).
--
-- Rollback: DROP TABLE events. Destructive and unrecoverable in a way the other
-- migrations are not: the aggregates hold current state, and this table is the
-- only record of how they got there. Back the file up first. Nothing in schema
-- 13 references it, so dropping it returns the database to schema 13 exactly.

-- sequence is the primary key so that a row's identity and its position in the
-- global order are one fact rather than two that could disagree (ADR-0003).
-- AUTOINCREMENT rather than a plain rowid because the sequence is a pagination
-- cursor: a reused position would hand a caller resuming from it an event it
-- had already read, or skip one it had not.
--
-- No foreign key names the aggregate. Every other table cascades from its
-- project, and an event that disappeared when its project was deleted would be
-- a timeline that rewrites itself — which is the thing INV-013 forbids. An
-- event may therefore outlive what it describes, and the aggregate id is
-- stored as plain text.
--
-- type is not constrained to a known set. Spec 16.4 requires consumers to
-- tolerate unknown event types, and a CHECK constraint would turn a downgrade,
-- or the older half of a rolling upgrade, into a log that cannot be opened.
-- The write side refuses unknown types; the schema does not need to as well,
-- and if it did, the refusal would land in the one place that cannot recover.
--
-- payload is opaque to this table on purpose: it is the one field the event
-- constructor cannot redact, because only the writer knows what is in it.
-- principal and metadata are redacted before they reach here (INV-012).
CREATE TABLE events (
    sequence       INTEGER PRIMARY KEY AUTOINCREMENT,
    id             TEXT NOT NULL UNIQUE,
    type           TEXT NOT NULL,
    version        INTEGER NOT NULL,
    aggregate_type TEXT NOT NULL,
    aggregate_id   TEXT NOT NULL,
    at             TEXT NOT NULL,               -- RFC3339Nano, UTC
    principal      TEXT NOT NULL,               -- JSON Principal, redacted
    correlation_id TEXT NOT NULL,
    causation_id   TEXT NOT NULL DEFAULT '',
    payload        TEXT NOT NULL DEFAULT '',    -- opaque JSON, '' when absent
    metadata       TEXT NOT NULL DEFAULT '{}'   -- JSON string map, redacted
);

-- Both indexes end in sequence because every read filters and then walks the
-- order: one index serves the whole query rather than narrowing the rows and
-- leaving a sort behind.
CREATE INDEX idx_events_aggregate ON events(aggregate_type, aggregate_id, sequence);
CREATE INDEX idx_events_correlation ON events(correlation_id, sequence);
