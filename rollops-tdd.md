# Rollops — Technical Design Document

*Rollout operations for the agentic web*
**Umbrella:** Klarlatz · **Status:** Dogfooded OSS design · **Companion to:** `rollops-vision.md`

---

## 1. Scope & Principles

This document specifies the architecture for Rollops: a lean, infrastructure-agnostic rollout orchestration system where agents and humans are peer operators. It builds entirely on the existing Klarlatz/OSS stack.

Design principles carried from the vision:

- **Lean as a forcing function** — single binary, runs on a bare Hetzner VPS, no Kubernetes dependency.
- **Everything configurable** — storage, targets, strategies, gates, and promotion paths are pluggable, not assumptions.
- **Agents and humans share one surface** — CLI, UI, and MCP are thin clients over a single engine.
- **Trust by default** — risk-gated, human-in-the-loop on sensitive changes, fully auditable.
- **Präzise** — typed contracts, loud validation, explicit state.

---

## 2. Stack Mapping

Rollops is assembled from existing components rather than built from scratch:

| Concern | Component | Role |
|---|---|---|
| Rollout lifecycle | **statekit** | Each rollout is a statechart: `pending → validating → deploying → verifying → promoted` / `rolled-back` |
| Step execution | **axi-go** | Safe, auditable execution kernel that actually runs each deployment step |
| Resilience | **fortify** | Retries, circuit breakers, rate limiting, bulkheads around every target operation |
| Risk scoring | **blast-radius gate** | Homegrown `internal/risk` score that drives the approval gate. decision-kit is a reserved pin, not the scorer. |
| Audit / events | **bolt** | Structured, compliance-grade audit and event logging (distinct from `bbolt`, which is intentionally **not** used to avoid the name clash) |
| Agent interface | **mcp-go** | The MCP server embedded in the daemon, exposing Rollops operations to agents |

Language: **Go** throughout. Config: **YAML + strict schema + CEL** for conditional logic.

---

## 3. High-Level Architecture

The **engine is a Go library** at the center. Every interface — CLI, daemon, MCP, UI — is a thin client over it. This is what makes the hybrid control model work: the one-shot CLI calls the engine in-process with no daemon required, while the daemon wraps the same engine behind a network API.

```
   Humans / CI ──CLI──▶ (one-shot: engine in-process | daemon mode: gRPC client)
   Agents ──────MCP (mcp-go)──▶  ROLLOPS DAEMON
   Browser ─────HTTP/JSON──▶

   ENGINE (Go library):
     Reconciler ──▶ statekit (rollout lifecycle) ──▶ blast-radius risk gate
     Store (iface: SQLite)   axi-go (step execution, wrapped in fortify)
                                       ──▶ Target (iface)
     bolt (audit log)

   compiled-in targets: K8s | SSH/VM | FTP        gRPC plugin escape hatch: community/custom
   Git repos (one per customer/service) ──poll──▶ Reconciler
     (HMAC webhook listener: Phase D of make-it-real; poll is the trigger today)
```

---

## 4. Control Model

**Hybrid, by design:**

- **Daemon mode** — a long-running reconciler watches Git, detects drift, and fires scheduled rollouts. The always-on brain for unattended operation.
- **One-shot mode** — every action is also invokable directly from the CLI, running the engine in-process. No hard dependency on the daemon being up; good for local use, CI, and recovery.

**Target reach is agentless:** the daemon/CLI pushes to each target over its native transport (kube-apiserver, SSH, FTP) from one control point. Nothing to install on targets. A host-side pull agent is out of scope.

**Trade-off:** the hybrid adds a little surface area (two execution paths over one engine) but removes the single point of failure and keeps the lean OSS story intact. The shared engine library keeps the two paths behaviourally identical.

---

## 5. Engine & Interfaces

### 5.1 Engine (library)
A single Go package exposing all operations: plan, apply, verify, promote, rollback, observe, schedule. Transport-agnostic and storage-agnostic. Both the daemon and the one-shot CLI link it directly.

### 5.2 Daemon
Wraps the engine behind **gRPC** (typed, ergonomic for CLI and agents) and an authenticated **HTTP/JSON** surface for browser and automation clients. Production deployments should terminate TLS/mTLS at the daemon boundary or a trusted reverse proxy; local development uses bearer tokens.

### 5.3 MCP server (mcp-go)
**Embedded in the daemon.** One process exposes the agent surface; there is no standalone MCP binary. MCP tools expose the safe agent operations: `rollouts.plan`, `rollouts.apply`, `rollouts.rollback`, `rollouts.status`, `rollouts.list`, `rollouts.history`, and `rollouts.drift`. Apply still requires plan-before-apply.

### 5.4 CLI
Two modes against the same engine: in-process (one-shot) or gRPC client (talking to a running daemon). Identical command surface.

### 5.5 UI
Read-and-act dashboard over the REST gateway: live rollout state, history, pending approvals, drift status.

---

## 6. Configuration Model

- **Surface format: YAML**, fluently written by both humans and agents.
- **Strict published schema + Go-side validation** that rejects malformed config loudly — no silent typos.
- **CEL (Common Expression Language)** embedded for conditional logic: risk-gate conditions, promotion criteria, rollback triggers. No bespoke DSL.
- **relicta's governance DSL stays separate** — different concern, evolves independently.

Each watched repo carries its own config at a configurable path/branch (see §10), keeping multi-tenancy a property of Git structure.

---

## 7. Target Plugin Interface

A single Go contract serves both rich and dumb targets:

```go
type Target interface {
    Apply(ctx context.Context, desired Manifest) (Result, error)   // idempotent
    Observe(ctx context.Context) (Fingerprint, error)              // normalized state
    Health(ctx context.Context) (HealthStatus, error)             // verify + auto-rollback
}
```

- **First-party targets** (Kubernetes, SSH/VM, FTP) are **compiled into the single binary** — lean common case.
- **Optional gRPC plugin escape hatch** (HashiCorp go-plugin style) lets the community ship exotic targets without forking core.
- **Conformance suite** — every target (first-party or plugin) must pass a shared test suite proving idempotency, fingerprint stability, and health semantics.

Every target operation is wrapped in **fortify** (retry, circuit-breaker, rate-limit) and executed through **axi-go** so each step is sandboxed and auditable.

---

## 8. Rollout Lifecycle (statekit)

```
pending → validating → [risk gate] → deploying → verifying → promoted
                          │                                    │
                          ▼                                    ▼
                   awaiting-approval                      rolled-back
```

- **validating** — schema + CEL validation, dependency resolution, and a **plan/diff** computed and surfaced before anything is applied. Apply on an agent-driven rollout requires the plan to have been produced.
- **risk gate** — `internal/risk` scores the change (§9). Below threshold → proceed; above/sensitive → `awaiting-approval`.
- **deploying** — progressive strategy executes: canary, blue-green, or rolling, with configurable traffic shifting.
- **verifying** — `Health()` + post-deploy smoke tests gate promotion.
- **rolled-back** — triggered manually, by an agent, or automatically (§11).

statekit gives durable, inspectable state for every in-flight rollout — persisted via the `Store`.

---

## 9. Risk Gate (blast-radius)

Blast-radius score from five **observability-free** signals:

1. **Target criticality** — operator-configured weight per service.
2. **Environment** — prod > staging > dev.
3. **Change type** — config < code < schema/DB migration.
4. **Blast radius** — count of downstream dependents from the dependency graph (§12).
5. **Rollout strategy** — full cutover riskier than a small canary.

Gate behaviour (configurable via CEL): below threshold → auto-proceed; above threshold or sensitive-flagged → single human approval (approve / reject / block). No multi-stage chains in v1. Historical failure-rate becomes a sixth signal (P1) once enough run data exists.

---

## 10. Git Integration

- **Trigger: poll today.** The daemon pulls watched repos on a tick; that poll **doubles as the drift-detection heartbeat**. HMAC signature verification exists as a library (`internal/git`); a GitHub webhook listener that fires immediate reconciliation is Phase D of make-it-real — do not document it as shipping until that lands.
- **Auth: GitHub App or deploy keys**, per repo.
- **Multi-tenancy: one repo per customer/service.** The daemon watches N repos, each with its own config at a configurable branch + path. Isolation is a property of Git structure.
- Git is the single source of **desired** state; observed state lives in the `Store`.

---

## 11. Reconciliation, Drift & Rollback

### Reconcile loop
On each poll tick: read desired state from Git → call `Observe()` on each target → diff. **Drift = desired fingerprint ≠ observed fingerprint.** On drift: detect, alert (bolt + notifications), and reconcile back to desired state, subject to the risk gate.

### Drift across heterogeneous targets
- **Rich targets** (K8s) implement `Observe()` natively against live state.
- **Dumb targets** (FTP, plain VM) verify a **manifest/checksum stamped at deploy time**. Verification depth is configurable: shallow marker check vs. full checksum.

### Rollback
Three modes — manual, automatic, agent-driven. v1 auto-rollback signal is observability-free:
- `Health()` check failure (HTTP / TCP / command exit), **or**
- a post-deploy **smoke test** failure ("run this, expect exit 0"), **or**
- a **step error / timeout** from axi-go.

Metric-based rollout analysis is the **Phase 2 seam** where Obvia technology can plug in. It is off by default; set `ROLLOPS_ANALYSIS=1` to evaluate `spec.analysis`.

---

## 12. Data Model & State Store

```go
type Store interface {
    SaveRollout(ctx, Rollout) error
    LoadRollout(ctx, id) (Rollout, error)
    ObservedState(ctx, targetRef) (Fingerprint, error)
    Schedule(ctx, ScheduledRollout) error
    DueSchedules(ctx, now) ([]ScheduledRollout, error)
    History(ctx, targetRef) ([]RolloutRecord, error)
}
```

Backends: **SQLite** (the shipped runtime Store — single-file, single-binary friendly). Postgres and mnemos backends are not implemented; do not document them as options.

Core entities: `Rollout`, `TargetState`, `ScheduledRollout`, `RolloutRecord`, `Dependency`.

**The `Store` is not the source of truth for desired state** — Git is. Desired state is always reconstructable from Git, so losing the Store degrades history and requires re-observation but never corrupts what *should* be deployed.

**Dependency ordering** is a DAG: services deploy independently by default, or in explicit chains. The same graph feeds the blast-radius signal in §9.

---

## 13. Secrets, Audit & Scheduling

- **Secrets** — never stored locally. Integrate established vaults via a `SecretProvider` interface; targets receive credentials at execution time only.
- **Audit** — built in from day one via **bolt**: every decision, approval, execution, and rollback logged, structured and traceable across layers.
- **Scheduling** — `ScheduledRollout` queue in the `Store`; the daemon's reconcile tick fires due schedules. Both humans and agents can queue a rollout for a future time.

---

## 14. Security & Trust

### 14.1 API authentication & authorization
- Every interface is authenticated. gRPC and the REST gateway require **mTLS or signed tokens**; no anonymous calls. The MCP surface is authenticated identically.
- **RBAC on operations.** `rollouts.apply` to prod is a distinct, grantable permission from `rollouts.status`. Roles are declarative and audited.
- One-shot CLI inherits the invoking user's local credentials; it cannot bypass the gate or RBAC.

### 14.2 Credential hygiene
- Least privilege per target/repo — no single god-credential. Git access read-only per repo via GitHub App or deploy key.
- Vault authentication uses workload identity where available, falling back to a vault-managed bootstrap secret — never plaintext on disk.

### 14.3 Secret redaction in the audit trail
- Secret material is redacted at the logging boundary — values from the `SecretProvider` are never serialized into logs, plans, diffs, or MCP responses. The trail records *that* a secret was used, never its value.

### 14.4 Artifact integrity
- Before deploy, Rollops verifies provenance and signature (cosign / SLSA-style) where the target supports it. Verification policy is configurable per target, with a secure default. Matters most for agent-driven rollouts.

### 14.5 Webhook verification
- HMAC-SHA256 verification for inbound GitHub webhooks exists as a library. The daemon has **no webhook route** yet (Phase D of make-it-real). Poll is the only reconcile trigger today.

### 14.6 Agent guardrails
- **Non-bypassable policy floor** — certain changes (prod schema migrations, critical-flagged targets) always require human approval regardless of computed risk. An agent cannot lower its own thresholds.
- **Emergency freeze / kill-switch** — a single operator action halts all in-flight and pending rollouts; agents cannot override it.
- **Full attribution** — every action records which identity initiated it, captured immutably via bolt.
- **Rate limiting** — agent-initiated rollouts are rate-limited via fortify.

---

## 15. Concurrency & Reliability *(proposed defaults — open for review)*

- **Per-target advisory locks** — two rollouts never operate on the same target concurrently.
- **Per-repo reconcile serialization** — one reconcile per repo at a time; independent repos/targets run in parallel up to a configurable limit.
- **fortify** governs flaky-target behaviour: retry with backoff, circuit-break, rate-limit.
- **Crash recovery** — statekit state persisted per transition, so an interrupted rollout resumes (or rolls back) deterministically.
- **Self-observability** — the daemon exposes its own health and Prometheus-style metrics (reconcile latency, drift count, rollout success/failure rates).

---

## 16. Deployment Topology

- **OSS / lean:** single Go binary + SQLite, agentless, on one Hetzner VPS. Daemon + embedded MCP + UI in one process.
- **Studio / scale:** a separate commercial layer (open-core boundary). The OSS binary does not grow a Postgres Store or a host agent for that path.

---

## 17. Open Items / Revisit as it grows

Active work is the **Make it real** program (`docs/design/make-it-real.md`,
Roady features `real-trust` / `real-agent` / `real-canary` / `real-gitops`).
That RFC is the near-roadmap. Do not add Argo checkboxes outside it.

Still true, and in scope there:

- **Metric-based analysis** — opt-in via `ROLLOPS_ANALYSIS=1`; default off.
  v1 rollback stays observability-free unless analysis is enabled.
- **Git webhook listener** — HMAC verify exists; the daemon has no route
  (Phase D). Poll is the only trigger today.
- **Canary pause/resume** — `Stepper` exists and is unused; engine still
  blocks in `Executor` (Phase C).

Out of this program (do not start): plugin keyless/Rekor, ApplicationSet
matrix, Jsonnet, app-of-apps, host agent, Postgres/mnemos backends, studio
billing, Istio/NGINX/SMI.
