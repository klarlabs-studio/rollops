# Roadmap — Rollops

## Current near roadmap — Make it real

Approved 2026-08-14. Canonical RFC: `docs/design/make-it-real.md`.
Roady features: `real-trust` → `real-agent` → `real-canary` → `real-gitops`.

The job is to make advertised behaviour real, then the agent USP, then
controllable canaries, then the smallest GitOps wedge that steals a small
Argo install. **Stop there.** Jsonnet, app-of-apps, ApplicationSet matrix,
host agent, Postgres, studio billing are out of scope.

### Phase A — Trust pass (docs = daemon)

- [x] Shared engine boot (`internal/boot`) for CLI and daemon
- [x] Persist `EvaluateRisk` onto `Rollout.RiskScore`; callers still pass zeros
- [ ] Wire metric analysis in `rollopsd` (opt-in, default off)
- [ ] Wire Vault+Env secret chain from `VAULT_ADDR`
- [x] Persist freeze across restart
- [ ] CLI cannot skip freeze/secrets/audit; gRPC client uses TLS when daemon does
- [ ] Traffic `SetWeight` failure aborts the step
- [ ] Image-automation `current` only when scanner identity equals Git pin (#98)
- [ ] Honesty docs last: strike Postgres/mnemos/host-agent/standalone-MCP/decisionkit UI

### Phase B — Agent-native dogfood

- [ ] Opt-in `agent:deploy` role (do not widen default `agent:*`)
- [ ] MCP `list` / `history` / `drift`
- [ ] Agent operator runbook (`ROLLOPS_MCP_ADDR` + tokens file + grant)

### Phase C — Canary as a verb

- [ ] Tick-driven `Stepper` with snapshot restore (replace blocking `Executor`)
- [ ] Pause / resume / abort on CLI, HTTP, gRPC, MCP
- [ ] Same controls in `/ui`
- [ ] `doctor`/`plan` name bake vs traffic canary and cutover vs blue-green

### Phase D — GitOps wedge

- [ ] GitHub HMAC webhook → `watcher.Tick`
- [ ] `RolloutSet` list generator (in-memory; no engine/store change)
- [ ] Kubernetes `ignoreDifferences`
- [ ] Reconcile waits on `dependsOn`

WIP: `max_wip: 3`. Finish a phase before opening the next. Do not start B
until A honesty-docs is done.

---

## Previous near roadmap — closed

P0/Roady is complete, verified, and drift-free. Near-roadmap release polish and
the Argo-like operator UI pass are closed as of 2026-06-09.

P0/Roady is complete, verified, and drift-free. Near-roadmap release polish and
the Argo-like operator UI pass are closed as of 2026-06-09.

### Argo-like operator UI

Goal: make `/ui` the default daily operator surface, not just a status page.

- [x] Application/target list with health, sync/drift, risk, phase, last actor, and
  last change at a glance.
- [x] Target detail that behaves like Argo's app detail: resource tree, rollout
  timeline, history, diff, live status, sync, rollback, approve/reject.
- [x] Clear sync/drift vocabulary: desired from Git, observed from target, runtime
  state from Store.
- [x] Better visual hierarchy: dense tables for fleets, detail panes for one target,
  explicit attention queue for approvals/drift/failures.
- [x] Live updates without page refresh; keep polling first unless server push proves
  necessary.
- [x] Preserve hard auth/RBAC boundaries: UI actions are the same operations as CLI,
  gRPC, HTTP, and MCP.

Closed pass: filterable dashboard, attention queue, dense application list,
derived health/sync/risk, target detail summary cards, graph/list resource
views, desired/live diff, rollout timeline, rollback/sync/approve/reject, and
responsive desktop/mobile layouts.

Next UI work should only enter P2/studio or be driven by dogfood findings.

## P2 / ecosystem

- Metric-based rollout analysis as a stable feature, likely the Obvia seam.
- Historical-failure risk signal.
- DB rollback workflows.
- Multi-instance coordination / leader election.
- SSO and enterprise auth integration.
- Image automation workflows.
- Managed multi-customer studio layer.
- Optional deeper observability, feature flag, and governance integrations.
- Multi-stage approval chains only if dogfood proves real demand.

---

# Historical — Rollops P0 build order

Roady has the 63 atomic tasks (1:1 per requirement, no inter-task deps wired).
This is the **phased dependency order** to execute them in. Build bottom-up:
contracts → backends → engine → lifecycle/gate → targets → reconcile/rollback →
interfaces → security → UI/obs.

## Phase A — Contracts & foundation
- [x] `target-contract` — Target interface + types *(scaffolded)*
- [x] `store-backend` — Store interface *(scaffolded; SQLite backend still TODO)*
- [ ] `config-model` — YAML schema v1 + version, Go validation, CEL hooks ← **start here**
- [ ] `store-backend` — SQLite backend + migrations + crash-safe persist

## Phase B — Engine core
- [ ] `engine-library` — plan/apply/verify/promote/rollback/observe/schedule; plan-diff; advisory locks
- [ ] `rollout-lifecycle` — statekit statechart, validating phase, plan-before-apply
- [ ] `step-execution` — axi-go + fortify wrapping of every Target op

## Phase C — Decision & delivery
- [ ] `risk-gate` — decision-kit 5-signal score, CEL threshold, single approval gate
- [ ] `progressive-delivery` — rolling / canary / blue-green + traffic shifting
- [ ] `dependency-ordering` — DAG, cycle detection, serialize chains / parallelize independents (also feeds risk blast-radius)

## Phase D — Targets
- [ ] `first-party-targets` — SSH/VM (dumb) first → FTP (dumb) → Kubernetes (rich)
- [ ] `target-contract` — conformance suite + gRPC plugin protocol

## Phase E — Reconcile, drift, rollback
- [ ] `git-integration` — webhook (HMAC) + poll, per-repo auth + config path
- [ ] `reconcile-drift` — reconcile loop, fingerprint diff, detect/alert/reconcile, verification depth
- [ ] `rollback` — manual/agent/auto (Health + smoke + step error/timeout); re-apply prior manifest
- [ ] `scheduling` — ScheduledRollout queue + due firing
- [ ] `environment-promotion` — staged + independent per-env configs

## Phase F — Cross-cutting trust
- [ ] `secrets` — SecretProvider + vault integration, workload identity
- [ ] `audit` — bolt structured log, secret redaction at boundary, attribution
- [ ] `security-trust` — RBAC, artifact provenance, webhook HMAC, agent guardrails (policy floor, kill-switch, rate-limit)

## Phase G — Interfaces
- [ ] `cli` — one-shot in-process + gRPC client, identical surface
- [ ] `daemon-api` — gRPC service + grpc-gateway REST + mTLS/token auth
- [ ] `mcp-server` — mcp-go, tools 1:1 to engine ops, embedded + standalone

## Phase H — Visibility
- [ ] `self-observability` — health endpoints + Prometheus metrics
- [ ] `ui-dashboard` — live state, history, approvals, drift

## Build-order rationale

- Security/audit (Phase F) are **cross-cutting**: stub the hooks from Phase B,
  fully implement in F before any interface is exposed. No interface ships unauthenticated.
- Targets: do a **dumb target (SSH/VM) first** — it exercises the stamped-manifest
  drift path and is the leanest end-to-end proof; K8s (rich Observe) last.
- A vertical slice (config → engine → SSH target → CLI apply) is the fastest path
  to a working demo; pull those tasks forward if a demo is needed before full breadth.
