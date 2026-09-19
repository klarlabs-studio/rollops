# ADR-0003 — Aggregates are authoritative; events are appended in the same transaction

- **Status:** accepted
- **Date:** 2026-09-19
- **Spec:** `docs/architecture/rollops-next.md` §16, §17, §18, §36 · **Backlog:** R2, R5
- **Invariants touched:** INV-005 (universal attribution), INV-006 (transport independence), INV-012 (secret non-persistence)
- **Decides:** pending decision #3

## Context

§16.1 asks for "an append-only domain event stream with projections" and says
plainly that this "does not require full event sourcing on day one". §17.2
asks for "transactional writes for aggregate state + outbox/event append
where required". §18.1 asks for revision-based optimistic concurrency.

Those three sentences leave the central question open: **when a use case
changes an aggregate and records what happened, what is the unit of work, and
which of the two is the source of truth?**

The answer gates R2, because it decides the shape of every repository port,
and it gates R5, because the timeline is read from whatever this produces.

There is also an inherited constraint. `internal/store/sqlite` has no version
table. `Open` re-executes all eleven migrations on every start, relying on
`CREATE TABLE IF NOT EXISTS` and on `applyAddColumns` swallowing SQLite's
"duplicate column" error. It works, but it cannot express a migration that is
not idempotent by construction, it cannot tell an empty database from a
current one, and it makes §36's required "upgrade test from latest released
schema" impossible to write — there is no schema version to upgrade from.

## Decision

### Aggregates are the source of truth. Events are a durable record of change.

Current state is read from aggregate tables, never folded from events. The
event log is append-only and authoritative for *what happened*, not for *what
is*. This is the §16.1 position — converge toward an event stream without
paying for event sourcing now — and it keeps a query like "which release is
in production" a single indexed row read.

Reversing this later is possible precisely because the log is complete: full
event sourcing becomes a change to how aggregates are loaded, not a data
migration.

### One transaction covers the aggregate write and the event append.

A command that changes an aggregate and records nothing, or records an event
whose aggregate write was rolled back, is a lie in the timeline. Both happen
or neither does.

This is enforced rather than documented: **the event appender fails if it is
called outside a transaction.** A repository may fall back to the connection
pool for reads; an append may not. That turns the invariant into an error at
the first test that forgets it.

### The transaction boundary is a port, and it carries no SQL.

```go
type Transactor interface {
    WithinTransaction(context.Context, func(context.Context) error) error
}
```

The application layer says "these writes are one unit"; it never sees
`*sql.Tx`. The SQLite adapter derives a context holding the transaction and
repositories read it back out. Contexts carrying values is a known wart, and
the alternative — handing every repository method a transaction argument —
would put `database/sql` in the ports and break INV-006. The narrower
alternative, a bundle type exposing every repository, is the "one giant
`Store` interface" §17.1 asks us to avoid.

Nesting is not supported. `WithinTransaction` inside `WithinTransaction`
reuses the existing transaction rather than opening a savepoint, because
partial rollback of half a command is not a behaviour any use case has asked
for and simulating it invites the bug it looks like it prevents.

### Optimistic concurrency is compare-and-set on `Revision`.

An update carries the revision the caller read, and the `WHERE` clause tests
it. Zero rows affected means someone else got there first, and the repository
returns a typed conflict — not a generic error and not a silent no-op, which
is what an unguarded `UPDATE` would produce.

`identity.Revision.Matches` already refuses a zero expected revision, so "I
did not read the aggregate first" fails as loudly as a genuine conflict.

### Event `Sequence` is global and monotonic, not per-aggregate.

§16.2 gives the envelope one `Sequence` field without saying which. It is
global, backed by an autoincrementing key.

The reason is that a second counter would be a duplicate fact. Per-aggregate
ordering already exists — `Revision` — and it is what concurrency control
uses. What only a global sequence provides is total order across aggregates,
which is exactly what the timeline renders and what cursor pagination needs
to page without gaps or repeats. Per-aggregate history stays correctly
ordered as a filtered scan of a globally ordered log.

### Projections are written synchronously, inside the same transaction.

§16.5 lists projections and §36 suggests a `projection_offsets` table. That
table is **not** created yet, and no catch-up worker is written.

A single binary against a single SQLite file has one writer. An asynchronous
projection there buys eventual consistency and a rebuild checkpoint, and
costs a worker, a lag metric, and a class of bug where the API reports state
that the write already superseded. Nothing yet needs what it buys.

The constraint that keeps this reversible: **a projection must be derivable
from retained data alone.** No projection may hold a fact that exists nowhere
else. Anything that does is an aggregate wearing a projection's name.

### Migrations get a version table.

A `schema_migrations` table records applied versions, and each migration runs
once, in order, in its own transaction.

Existing databases are **baselined, not re-migrated** — but the baseline is
established by running the legacy path first, not by inferring it.

The tempting shortcut is to look at the database, conclude "this has the
legacy tables, so it is at schema 11", and record that. It is not safe. A
database last opened by a build that shipped only nine migrations is at nine,
and nothing in it says so; baselining it to eleven would skip two migrations
and leave columns missing. The legacy path is idempotent by construction and
brings any such database up to eleven, so running it first turns schema 11
from an inference into a fact. Only then are versions 1–11 recorded.

New migrations start at 12, run once each, in order, in their own
transaction. Per §36 each carries an up migration, a documented rollback
strategy, and a test that upgrades from the previous released schema. The
ad-hoc `applyAddColumns` path is retained solely to serve that baseline and
takes no new migrations.

## Consequences

- Every repository port is free of `database/sql`, so PostgreSQL (§17.4) and
  the in-memory fakes used in tests implement the same interfaces.
- A use case that forgets its transaction fails at its first event append
  rather than producing a silently untimelined mutation.
- Attribution (INV-005) and redaction (INV-012) belong to the envelope, not
  to each call site: the appender takes a `Principal` and stores it
  `Redacted()`. A caller cannot append an unattributed event because there is
  no method that omits it.
- The global sequence is a single hot integer. On SQLite with one writer this
  is free. On PostgreSQL with concurrent writers it becomes a contention
  point, and a gapless sequence there needs care — that is a cost of §17.4,
  to be paid when §17.4 is.
- The legacy path keeps running on every open for as long as the baseline
  matters. It is eleven cheap idempotent statements, and retiring it requires
  a released version floor below which upgrades are not supported — which is
  a decision for whenever 1.0 is.
- Synchronous projections mean a write transaction grows with the number of
  projections it touches. This is the thing to watch; when a write gets slow,
  that is the signal to revisit, and `projection_offsets` is the shape of the
  answer.

## Alternatives rejected

- **Full event sourcing now.** §16.1 explicitly does not ask for it. It would
  make every read a fold, force snapshotting sooner than the data justifies,
  and turn every schema change into an upcaster.
- **Events appended outside the aggregate transaction.** Cheaper writes, and
  the timeline becomes a best-effort log that disagrees with state after any
  crash. The audit story is a large part of what this product is.
- **A transaction argument on every repository method.** Honest about what is
  happening, and it puts `*sql.Tx` — or a leaky abstraction over it — into
  the ports, which INV-006 forbids.
- **A `Repositories` bundle handed to a callback.** Avoids the context wart
  but recreates the giant store interface §17.1 warns against, and couples
  every use case to the full set.
- **Per-aggregate event sequences.** Duplicates what `Revision` already
  tracks and gives the timeline no total order to page by.
- **Keep the current migration scheme.** It cannot express a non-idempotent
  migration, and §36 requires an upgrade test that needs a version to start
  from.
