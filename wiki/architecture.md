---
updated: 2026-06-08
---

# Architecture (reference)

Canonical source: `rollops-tdd.md`. This is the quick map.

## Shape

Engine is a **Go library** at the center. CLI, daemon, MCP, UI are thin clients.
- One-shot CLI → engine **in-process** (no daemon).
- Daemon → same engine behind **gRPC** + **grpc-gateway REST** + **embedded MCP**.
Both paths share the library, so they stay behaviourally identical and there's no single point of failure.

## Core contracts (in code)

- `pkg/target.Target` — `Apply` (idempotent), `Observe` (stable fingerprint), `Health`. The "infrastructure-agnostic" seam. Every target passes `pkg/conformance`.
- `store.Store` — runtime state only (rollouts, observed fingerprints, schedules, history). **Git is desired-state truth**, never the Store.

## Data flow

Git (desired) ──webhook+poll──▶ Reconciler ──▶ statekit lifecycle ──▶ risk gate (decision-kit) ──▶ deploy (axi-go in fortify) via Target ──▶ verify (Health + smoke) ──▶ promoted / rolled-back. bolt audits every step.

## Rollout lifecycle

`pending → validating → [risk gate] → deploying → verifying → promoted`,
branching to `awaiting-approval` (gate) and `rolled-back` (manual/agent/auto).

## Drift

`drift = desired fingerprint != observed fingerprint`. Rich targets (K8s) Observe live; dumb targets (FTP, VM) verify a stamped manifest/checksum. Poll doubles as the drift heartbeat.

## Topology

- **Lean/OSS:** single binary + SQLite, agentless, one VPS. Daemon + MCP + UI in one process.
- **Studio/scale:** same binary, Postgres, multiple coordinating instances; managed multi-customer layer above (open-core boundary).
