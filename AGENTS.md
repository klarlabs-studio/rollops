# Rollops — Project Identity

**The open-source control plane for shipping software.** Infrastructure-agnostic,
small-core, agent-first. Leaner alternative to ArgoCD/Flux; no Kubernetes
dependency; runs on a bare Hetzner VPS.

The north-star abstraction is **a Release moving safely through environments**:

```text
Source → Pipeline → Artifact → Release → Environment → Deployment
       → Verification → Promotion / Rollback
```

- **Module:** `go.klarlabs.de/rollops` (repo dir is `rollops`)
- **Language:** Go 1.26
- **Umbrella:** Klarlatz · **Brand DNA:** Smart, Präzise, Wertig, Verlässlich

## Canon

`docs/architecture/rollops-next.md` is the **authority for the intended
architecture**. Read it before changing anything structural. The repository is
the authority for current implementation. Where the two disagree, the repo is
behind, not the spec — unless a newer accepted ADR says otherwise.

`docs/legacy/` holds the superseded v0.x vision and TDD. They still describe the
implemented baseline accurately, which is why they were kept; they do **not**
describe where the product is going. Do not cite them as direction.

The spec must not be reinterpreted as a GitHub Actions clone, a Kubernetes-only
GitOps controller, a hosted CI SaaS, a YAML DSL framework, or an AI wrapper.

## Working Style

- **TDD, red-green-refactor.** Test first; table-driven Go tests. Every Target
  implementation targets the conformance suite.
- **Atomic conventional commits** (`feat:`, `fix:`, `test:`, `docs:`…).
- **Solution-first.** Fix root causes; no workarounds except for external blocks.
- **Packages by domain, not layer.** Small functions, self-documenting names.
- **Lean is a feature.** Every addition justifies its weight. Single binary +
  SQLite is the common case.
- **Warden locally.** Same gate as CI: `warden run pre-commit` (lint) and
  `warden run pre-push` (race tests + lint). Do not `--no-verify`.
- Memory is **mnemos**, not files in the repo. The old `memory/` directory was
  removed in the revamp; do not recreate it.
- Roady is the planning tracker, but **its current plan is stale** — 161/161
  tasks verified against the superseded v0.x spec. Regenerate it against the
  phases in the architecture doc before trusting `roady status`.

## Architectural Invariants (spec §40)

Hard constraints. Breaking one is an ADR, not a judgement call.

| ID  | Invariant                                                              |
| --- | ---------------------------------------------------------------------- |
| 001 | Planning does not mutate deployment targets                            |
| 002 | A Release's artifact/source identity never changes after creation      |
| 003 | Deployments operate on immutable artifact digests                      |
| 004 | Every caller type — human, CI, webhook, agent — uses one policy engine |
| 005 | Every mutation has a Principal                                         |
| 006 | Domain behaviour does not depend on CLI/API/MCP transport              |
| 007 | Core domain packages import no Kubernetes/cloud provider SDKs          |
| 008 | Supported local workflows remain possible in-process, without a daemon |
| 009 | GitOps is an integration, not the only mutation path                   |
| 010 | Apply completion alone does not imply verified success                 |
| 011 | Apply never silently regenerates a stale plan                          |
| 012 | Secret values never enter plans, events, audit, logs, or public state  |
| 013 | Persisted domain events are append-only                                |
| 014 | Target/executor features are capability-declared, never name-inferred  |
| 015 | Mutation APIs support safe replay semantics                            |
| 016 | Legacy RolloutConfig is translated, not abruptly discarded             |

## Work Rules (spec §38)

Before changing code: read the architecture doc, read this file, inspect the
existing packages, and identify **which phase (A–J) or backlog item (R1–R14)**
the change belongs to. State the affected invariants in the PR description.
Prefer adapting existing code over parallel replacement. Avoid drive-by
refactors.

Do **not**:

- add a storage backend abstraction when an existing port extends cleanly;
- duplicate domain models inside transport packages;
- put Kubernetes types in core domain APIs;
- return secrets in errors;
- mutate state during planning;
- bypass policy for MCP;
- make daemon availability mandatory for local commands;
- add YAML fields without corresponding typed domain semantics;
- introduce an executor before Phase H just to run target operations;
- silently break existing RolloutConfig files.

## Definition of Done (spec §39)

A feature is done when the applicable items hold: domain semantics defined ·
invariants tested · public API types documented · storage migration included ·
CLI machine output, HTTP/gRPC mapping, MCP mapping, RBAC permission and policy
decision point all considered · audit event emitted · secrets redacted ·
cancellation handled · idempotency handled for mutations · metrics/traces where
operationally relevant · conformance updated if a contract changed · backwards
compatibility tested · docs and examples updated.

## Current Position

The domain foundation (Phase A) has not started. Execute the backlog in order:
R1 typed identity → R2 Project/Environment/Artifact/Release persistence → R3
DeploymentPlan v2 → R4 wrap the existing engine in a DeploymentService → R5
event envelope and timeline. Pipeline/Executor work (Phase H onward) starts only
after R1–R14 are stable.

Everything listed in spec §2 — the existing engine, targets, conformance suite,
risk gates, progressive delivery, drift/reconcile, RBAC, guardrails, secret
providers, leases — is an **asset to migrate deliberately**, not to duplicate
and not to discard.

## Known Failure Modes

- Building a second, parallel delivery engine beside the existing one instead of
  wrapping it. Migration is incremental; there is no flag day (spec §35.4).
- Treating the Store as desired-state truth. It is not — Git or the API is.
  Losing the Store must never corrupt what should be deployed.
- Targets that aren't idempotent or have unstable fingerprints → the conformance
  suite must catch this.
- Leaking secret material into plans, diffs, logs, or MCP responses. Including
  git tokens in command args and error messages — redact `http.extraheader`.
  **Never let a credential reach the agent session:** create k8s secrets in your
  own terminal with `read -rs`; reference secrets by name only. A token pasted
  into a chat command lands in the transcript and is compromised.
- Letting the daemon become a single point of failure — the one-shot in-process
  path must stay behaviourally identical. One unreachable watched repo must not
  crash the daemon; one app's scan failure must not block its reconcile.
- Multi-org git auth with a fine-grained PAT — single-owner + selected-repos, it
  silently 403s on other orgs and private repos (symptom: only PUBLIC repos
  clone). Use a classic PAT (`repo` + `read:packages`) or a GitHub App. A
  "recreated" secret that is stale means `create` errored — check
  `creationTimestamp`.

## Stack

Build on the pinned stack, don't reinvent (see `internal/stack/stack.go`):
`go.klarlabs.de/{statekit,axi,fortify,bolt,mcp,mnemos}` +
`github.com/felixgeelhaar/decisionkit` (risk gate). Note `axi`/`mcp`, not `-go`.
Risk feeds the policy engine as an **input** (spec §12.4) — it is not a parallel
authorization system. CEL stays the only bounded expression language; a second
one requires an ADR.
