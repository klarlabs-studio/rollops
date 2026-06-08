# Roadmap — Rolloffs P0 build order

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
