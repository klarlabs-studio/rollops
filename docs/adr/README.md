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

| #   | Decision                                                              | Blocks             | Record   |
| --- | --------------------------------------------------------------------- | ------------------ | -------- |
| 1   | UUIDv7 vs ULID for typed domain IDs                                    | R1, Phase A        | ADR-0001 |
| 2   | Event persistence in SQLite vs a dedicated Bolt continuation           | R5, Phase B        | —        |
| 3   | Aggregate/projection transaction model                                 | R5, Phase B        | ADR-0003 |
| 4   | Target plugin RPC protocol (v2)                                        | R6, Phase D        | —        |
| 5   | Config API version naming and domain (`rollops.dev/v1alpha1`)          | R14, Phase E       | —        |
| 6   | Remote executor transport                                              | Phase J            | —        |
| 7   | Artifact store abstraction boundaries                                  | R2, Phase A        | ADR-0004 |
| 8   | OpenTelemetry semantic conventions                                     | observability §28  | —        |
| 9   | PostgreSQL support threshold                                           | scale §3.3         | —        |
| 10  | Whether pipeline definitions live in the Project resource or separately | Phase H           | —        |

Numbering note: the records are numbered in the order they were written, not
by the §41 list. Decision #3 is ADR-0003 by coincidence; decision #7 is
ADR-0004.

Decision 2 is the next one R5 needs.

## Decisions the spec did not anticipate

The spec also uses types it never defines. Those need a record too, and it is
listed here rather than above because §41 does not name it.

| Decision                                            | Blocks | Record   |
| --------------------------------------------------- | ------ | -------- |
| Shape of `TargetBinding`, `PolicyBinding`, lifecycle | R2     | ADR-0002 |
