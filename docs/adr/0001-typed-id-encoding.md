# ADR-0001 — Typed domain IDs are prefixed UUIDv7

- **Status:** accepted
- **Date:** 2026-09-19
- **Spec:** `docs/architecture/rollops-next.md` §4.2, §29 · **Backlog:** R1
- **Invariants touched:** INV-006 (transport independence)

## Context

The architecture spec requires opaque typed IDs that never expose database row
identity, are stable across CLI/HTTP/gRPC/MCP, and "SHOULD use UUIDv7 or ULID".
The choice gates R1 and is expensive to reverse: once identifiers are persisted
and handed to API clients they are permanent public surface.

What exists today is not a candidate. `internal/engine` generates rollout IDs as
`"ro-" + now.Format("20060102T150405.000000000")` — a timestamp with no entropy.
Two rollouts created inside the same nanosecond collide, and the identifier
leaks creation time at full precision into every surface that renders it.

Both candidates are time-ordered, which is what matters for storage: SQLite
primary-key inserts stay append-mostly instead of scattering across the B-tree,
and "most recent" queries can order by ID.

The deciding differences:

|                     | UUIDv7                        | ULID                         |
| ------------------- | ----------------------------- | ---------------------------- |
| Dependency          | `github.com/google/uuid`, **already in the module graph** (indirect) | `oklog/ulid`, a new direct dependency |
| Standard            | RFC 9562                      | de-facto spec, no RFC        |
| Text length         | 36 chars                      | 26 chars                     |
| Text sort = time    | yes                           | yes                          |
| Case                | lowercase hex                 | uppercase base32, case-insensitive |

## Decision

Domain IDs are **UUIDv7, rendered as a type prefix plus the canonical UUID
string**, e.g. `prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001`.

- The generator is `github.com/google/uuid`, promoted from indirect to direct.
- Each aggregate gets a distinct prefix: `prj_ env_ art_ rel_ dep_ pln_ vrf_
  run_ exe_ evt_`.
- IDs are stored and transported as TEXT. No binary column, no integer surrogate
  exposed.
- Generation goes through an injected `Generator` port so tests are
  deterministic (spec §29). Nothing calls `uuid.NewV7()` outside the adapter.

"Lean is a feature" decides this. ULID's shorter string is a real ergonomic win
in CLI output, but it is not worth a new direct dependency when the module graph
already carries a maintained, RFC-conformant generator that satisfies every
requirement in §4.2.

## Consequences

- The prefix carries the type, so a misrouted ID fails at parse rather than
  finding a foreign row. `ParseProjectID` rejects `env_…` even though both are
  strings underneath.
- UUIDv7 embeds a millisecond timestamp. This is **not** a confidentiality
  boundary: creation time is recoverable from any ID. Acceptable — creation time
  is already public in the event timeline — but IDs must never be used where
  unguessability is the security property. Anything needing that (tokens, signed
  URLs) uses a CSPRNG, not this package.
- 36 characters is verbose in a terminal. The CLI may render a short prefix for
  humans, but MUST emit the full ID in `--output json` (spec §26.3).
- Migration of the existing `ro-…` rollout IDs is deliberately **not** in scope
  here. Legacy rollouts keep their identifiers; the new `DeploymentID` is a
  different aggregate reached through the translator (spec §35). Any later
  backfill gets its own ADR.

## Alternatives rejected

- **ULID** — better ergonomics, new dependency, no RFC. Revisit only if ID
  length becomes a demonstrated usability problem.
- **UUIDv4** — no time ordering, so index locality and "latest" ordering are
  lost. No compensating benefit at our scale.
- **Integer surrogates** — forbidden by §4.2; couples public identity to
  storage and breaks the future multi-tenant path.
- **Keep the timestamp scheme** — collides under concurrency and leaks
  nanosecond timing. It is the thing being replaced.
