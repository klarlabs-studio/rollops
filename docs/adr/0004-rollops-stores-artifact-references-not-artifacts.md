# ADR-0004 — RollOps stores artifact references, never artifact content

- **Status:** accepted
- **Date:** 2026-09-19
- **Spec:** `docs/architecture/rollops-next.md` §4.5, §5, §12, §21 · **Backlog:** R2
- **Invariants touched:** INV-002 (artifact immutability), INV-007 (infrastructure independence)
- **Decides:** pending decision #7

## Context

§4.5 requires artifact identity to be immutable and gives the record a
`Digest` and a `Locator`. It does not say who holds the bytes those describe,
and that omission is decision #7 — "artifact store abstraction boundaries" —
which gates R2, because it decides whether the `artifacts` table is a catalogue
or a warehouse.

The question is live because the two readings lead to very different products.
A control plane that stores artifact content needs retention policy, garbage
collection, disk accounting, content-addressed layout, and an upload path — and
it becomes a registry that must be backed up or the releases it describes stop
being deployable.

## Decision

**RollOps records where an artifact is and what it must hash to. It never
holds the bytes.**

Artifact content lives where it was produced: an OCI registry, an object
store, a filesystem path. RollOps stores the `Locator` that addresses it and
the `Digest` that proves it, and nothing else about it.

This splits into two ports, which is the abstraction boundary the decision
asks for:

- **`ArtifactRepository`** — metadata rows. SQLite, alongside every other
  aggregate, under ADR-0003's transaction model.
- **`ArtifactResolver`** — content, one implementation per locator scheme.
  Used only at the moment a target needs bytes, and never by the domain.

The domain depends on neither. `artifact.Artifact` already carries the whole
record as inert data, and `validateLocator` already requires a remotely
resolved locator to be pinned to the artifact's own digest — so a stored
reference cannot drift to different content while keeping its identity
(INV-002).

**Registration does not download.** Registering an artifact records a claim:
this locator holds this digest. It does not fetch and verify, because doing so
would make registration cost the size of the artifact and require RollOps to
hold credentials for every registry at the moment of registration rather than
at the moment of deployment.

Verification happens where the bytes already are. A target fetching by pinned
digest gets verification from the registry protocol for free. Stronger checks
— signature, provenance, SBOM presence — are policy requirements (§12.2),
evaluated against the `DocumentRef`s the artifact carries, by an engine that
may use `ArtifactResolver` if it needs to read them.

## Consequences

- The `artifacts` table stays small and its rows stay cheap. A RollOps
  database is metadata; losing it loses history, not deployable software.
- There is no artifact garbage collection, retention policy, or disk quota in
  core, because there is nothing to collect. Registry lifecycle stays the
  registry's problem — which is where the credentials and the policy already
  are.
- A locator can rot. An artifact referencing a deleted image is a valid
  record that fails at deploy. This is the honest failure: RollOps never
  promised to keep the bytes, and a stale reference is discoverable by a
  resolver health check rather than papered over by a cache.
- Offline or air-gapped operation is a property of the registry, not of
  RollOps. Mirroring is configured in the target's registry settings.
- §21's Phase 2 build jobs change nothing here. A job produces an artifact
  and pushes it; the engine registers the reference it was told about, which
  is why §21 insists outputs are declared rather than parsed out of logs.
- `ArtifactResolver` is not needed for R2 at all. R2 persists references; the
  resolver arrives with the first thing that must read content.

## Alternatives rejected

- **A content-addressed artifact store in core.** Turns a control plane into
  a registry, with backup, GC and quota obligations, and duplicates
  infrastructure every user already runs. Contradicts INV-007 and the lean
  single-binary story.
- **Verify by download at registration.** Registration cost scales with
  artifact size, needs registry credentials at the wrong moment, and proves
  something that is re-proved for free at deploy time by a digest-pinned
  pull.
- **One `ArtifactStore` port covering metadata and content.** Couples a
  SQLite row write to a network fetch and makes the in-memory test fake carry
  bytes it has no use for.
- **Cache artifact content opportunistically.** A cache that can serve a
  deployment is a store with a different name, and it makes "where did these
  bytes come from" ambiguous at exactly the moment provenance matters.
