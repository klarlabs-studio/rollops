# ADR-0005 — Domain events live in SQLite; there is no Bolt store to continue

- **Status:** accepted
- **Date:** 2026-09-19
- **Spec:** `docs/architecture/rollops-next.md` §16, §17.1, §17.3, §36 · **Backlog:** R5
- **Invariants touched:** INV-012 (secret non-persistence), INV-013 (persisted domain events are append-only)
- **Decides:** pending decision #2
- **Amends:** spec §17.3, which rests on a false premise (see Consequences)

## Context

§41 names the open question as "event persistence in SQLite vs dedicated Bolt
continuation", and §17.3 sets a migration policy around it: existing Bolt audit
data **MUST NOT** be silently discarded, with a dual-read compatibility period
and an optional import of old records as legacy timeline entries.

That framing assumes RollOps has an embedded key-value store holding audit
history. **It does not.** `go.mod` requires `go.klarlabs.de/bolt v1.6.0` — a
klarlabs structured *logging* library, a zero-allocation `slog.Handler`. There
is no `go.etcd.io/bbolt` anywhere in the module graph.

What actually exists is `internal/audit` (117 lines): a `Logger` wrapping
`bolt.New(bolt.NewJSONHandler(w))`, whose `Record(Entry)` emits one JSON line
to an `io.Writer`. It has no reader, no index, no file format and no retention.
`cmd/rollopsd` passes `os.Stderr`; `internal/boot` passes `logOrDiscard(c.Log)`,
which is `io.Discard` when unset. In the default configuration the data §17.3
forbids discarding is discarded already.

So the choice §41 poses is not between two stores. It is between a durable
event log and a log line.

## Decision

### Domain events are rows in SQLite, in the same database as the aggregates.

ADR-0003 already decided this without naming it, and its title says so:
*aggregates are authoritative; events are appended in the same transaction.*

> **One transaction covers the aggregate write and the event append.** … Both
> happen or neither does.

> This is enforced rather than documented: **the event appender fails if it is
> called outside a transaction.**

A store outside the SQLite connection cannot join that transaction. Nor can a
`slog.Handler`: it has no transaction, no rollback, and nothing to roll back
to. Any separate event store reduces the guarantee to best-effort, which
ADR-0003 considered and rejected on the record —

> **Events appended outside the aggregate transaction.** Cheaper writes, and
> the timeline becomes a best-effort log that disagrees with state after any
> crash. The audit story is a large part of what this product is.

This ADR therefore ratifies rather than chooses. It exists because §41 lists
the question as open and R5 should not start with a contradiction between the
backlog and an accepted ADR. The spec's own layout (§17.1) already points the
same way, naming `eventstore/sqlite/` the "preferred convergence target".

Two further requirements follow from the transaction and are not incidental:
`Sequence` is gapless and monotonic per ADR-0003's global autoincrement, which
a log stream cannot offer; and events are redacted before persistence
(INV-012), which is a property of the writer, not of the sink.

### The audit logger stays, and stops being called durable.

`internal/audit` is a useful operational log and is not replaced by the event
table. What it is not — and what §17.3 assumed it was — is a record anyone can
read back. It keeps its job; it loses the implication that history lives in it.

### Append-only is a property of the port, not a rule in a document.

INV-013 is enforced the way plan immutability already is: `EventAppender`
declares `Append`, `EventReader` declares the reads, and neither declares an
update or a delete. There is no method to call, so there is no call site to
review. A rewritten event is the one thing a timeline cannot survive, and a
convention that only holds while everyone remembers it is not a guarantee.

### The event schema lands as migration 0014.

Migrations are versioned per ADR-0003 (`schema_migrations`, `applyVersioned`),
and 0012 and 0013 are taken.

## Consequences

- **§17.3 must be corrected, not implemented.** Its migration plan is
  unimplementable as written: there is no store to dual-read, and step 3
  ("optionally import old audit records") has no source. Attempting it would
  produce a compatibility shim for data that never existed. The spec is amended
  in the same change as this record.
- **No migration, no compatibility period, no dual-read.** R5 writes a new
  table and starts from empty. Whatever an operator has today is stderr, and
  stderr is not a schema — nothing is silently discarded because nothing was
  ever durably kept.
- **The write path costs what ADR-0003 said it costs.** One hot autoincrement
  and synchronous projections inside the write transaction. Free at
  single-writer WAL scale; a named contention point if §17.4's PostgreSQL
  option is ever taken.
- **The `eventstore/bolt/` directory in §17.1's layout is not created.** It was
  scaffolding for a migration that has no source.
- **The database now grows without bound.** Aggregate tables grow with the
  estate; the event log grows with everything that has ever happened to it, and
  a deployment's per-operation events will dominate it. §16.5 already concedes
  this by saying a projection can be rebuilt from retained events "where
  practical". What the retention window is, and which projections can survive
  losing the history behind them, is deliberately not decided here — it becomes
  answerable once there is a real event volume to measure, and guessing now
  would set a limit nobody can justify.

## Alternatives rejected

- **A dedicated embedded key-value store for events** (adopting bbolt for the
  purpose §41 imagined). It would have to be introduced, not continued, and it
  cannot share the aggregate transaction. That is ADR-0003's rejected
  alternative wearing a dependency.
- **Treating the JSON log as the event stream.** No ordering guarantee, no
  gapless sequence, no query, no redaction guarantee at the sink, and the
  default writer throws it away. It cannot serve
  `GET /v2/deployments/{id}/events` at all.
- **Implementing §17.3 literally anyway**, reading whatever JSON lines an
  operator happened to capture. It would import unstructured text of unknown
  provenance into a log the product asks people to trust, which is worse than
  starting empty.
