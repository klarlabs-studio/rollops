---
updated: 2026-08-14
---

# Architecture (reference)

Canonical source: `rollops-tdd.md`. This is the quick map.

## Shape

Engine is a **Go library** at the center. CLI, daemon, MCP, UI are thin clients.
- One-shot CLI → engine **in-process** (no daemon).
- Daemon → same engine behind **gRPC** + **HTTP/JSON** + **embedded MCP**.
Both paths share the library, so they stay behaviourally identical and there's no single point of failure. MCP is embedded; there is no standalone MCP binary.

## Core contracts (in code)

- `pkg/target.Target` — `Apply` (idempotent), `Observe` (stable fingerprint), `Health`. The "infrastructure-agnostic" seam. Every target passes `pkg/conformance`.
- `store.Store` — runtime state only (rollouts, observed fingerprints, schedules, history). **Git is desired-state truth**, never the Store. The shipped backend is SQLite.

## Data flow

Git (desired) ──poll / HMAC webhook──▶ Reconciler ──▶ statekit lifecycle ──▶ blast-radius risk gate ──▶ deploy (axi-go in fortify) via Target ──▶ verify (Health + smoke) ──▶ promoted / rolled-back. bolt audits every step.

`POST /v1/hooks/github` HMAC-ticks matching watched repos; poll is the safety net.

## Rollout lifecycle

`pending → validating → [risk gate] → deploying → verifying → promoted`,
branching to `awaiting-approval` (gate) and `rolled-back` (manual/agent/auto).

## Drift

`drift = desired fingerprint != observed fingerprint`. Rich targets (K8s) Observe live; dumb targets (FTP, VM) verify a stamped manifest/checksum. Poll doubles as the drift heartbeat.

## Topology

- **Lean/OSS:** single binary + SQLite, agentless, one VPS. Daemon + embedded MCP + UI in one process. No host agent, no Postgres/mnemos Store.
- **Studio/scale:** a separate commercial layer (open-core boundary), not a second Store backend in this binary.
