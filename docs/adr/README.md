# Architecture Decision Records

An ADR is required before introducing a new architectural primitive that
overlaps an existing one, and before relaxing any invariant in
[`../architecture/rollops-next.md`](../architecture/rollops-next.md) §40.

ADRs do **not** reopen the product principles (spec §1.4) without explicit
maintainer intent. Supersede an ADR rather than rewriting it.

## Format

`NNNN-short-kebab-title.md`, numbered sequentially from 0001. Each record
carries: status (`proposed` | `accepted` | `superseded by NNNN`), date, context,
decision, consequences, and the invariants or spec sections it touches.

## Pending decisions (spec §41)

These are named by the architecture spec and are not yet decided. Anyone
starting the relevant workstream writes the ADR first.

| #   | Decision                                                              | Blocks             |
| --- | --------------------------------------------------------------------- | ------------------ |
| 1   | UUIDv7 vs ULID for typed domain IDs                                    | R1, Phase A        |
| 2   | Event persistence in SQLite vs a dedicated Bolt continuation           | R5, Phase B        |
| 3   | Aggregate/projection transaction model                                 | R5, Phase B        |
| 4   | Target plugin RPC protocol (v2)                                        | R6, Phase D        |
| 5   | Config API version naming and domain (`rollops.dev/v1alpha1`)          | R14, Phase E       |
| 6   | Remote executor transport                                              | Phase J            |
| 7   | Artifact store abstraction boundaries                                  | R2, Phase A        |
| 8   | OpenTelemetry semantic conventions                                     | observability §28  |
| 9   | PostgreSQL support threshold                                           | scale §3.3         |
| 10  | Whether pipeline definitions live in the Project resource or separately | Phase H           |

Decisions 1, 3 and 7 gate the earliest work (R1–R2) and should be written
first — the typed-ID choice in particular is expensive to reverse once
identifiers are persisted and exposed across API surfaces.
