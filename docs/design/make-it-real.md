# RFC: Make it real

Status: **Approved for execution** — this is the near-roadmap.
Date: 2026-08-14.
Companion: `.roady/spec.yaml` features `real-trust`, `real-agent`, `real-canary`, `real-gitops`.

## Problem

Rollops is past MVP and dogfooded (v0.30.0, in-cluster fleet). The engine,
targets, Git watch, image automation, UI, MCP, and guardrails exist. Several
advertised capabilities are **library code the daemon never turns on**, or
**docs/UI claims the runtime does not keep**. That is a trust bug for a product
whose brand is Präzise / Verlässlich, and it is the main thing standing between
"works for us" and "fully competitive in the category we actually occupy."

The category is not "Argo CD but smaller." It is:

> The GitOps control plane for mixed infrastructure where agents and humans
> share one audited engine, and Kubernetes is a target, not the universe.

## Non-goals (this program)

Do **not** pull these in. They are gravity toward a worse Argo.

- Jsonnet, app-of-apps, ApplicationSet matrix/git generators, Helm *install*
  (we stay on `helm template` + apply; document that loudly)
- Host agent / pull mode
- Postgres or mnemos Store backends
- Studio / billing / multi-customer tenancy
- Istio, NGINX, SMI, replica-weighted canary (Gateway API remains the rich path)
- Multi-stage approval chains
- Replacing the homegrown blast-radius scorer with decisionkit unless we actually
  call it (the pin is currently a blank import)

## Current vs intended (honesty table)

| Claim today | Intended after this program |
|---|---|
| decisionkit risk in the UI | Homegrown score **persisted on the rollout** and shown as such; pin used or dropped |
| `spec.analysis` in YAML | Daemon **evaluates** it when configured; still off by default |
| Vault SecretProvider | Daemon **constructs** Vault+Env chain from env |
| Postgres / mnemos backends | Struck from README/TDD until they exist |
| Git webhook + poll | Webhook **listener** ships; poll stays the safety net |
| Standalone MCP binary | Struck; embedded MCP is the product, **documented and dogfoodable** |
| Host agent | Struck |
| Canary / blue-green | Canary is pause/resume/abort **or** an honest bake; blue-green without traffic routing is named a cutover |
| One-shot CLI ≡ daemon | Shared `boot` options; CLI cannot skip freeze/secrets/audit |
| Freeze / agent rate-limit | Freeze **survives restart**; limiter stays in-memory (acceptable) |
| `current` image verdict | Legal only when scanner identity **equals** Git pin, both logged |

## Phases

Execute in order A → D. Tasks inside a phase may parallelize when
`depends_on` allows. TDD: test first, table-driven, then the wiring.

---

### Phase A — Trust pass

**Goal:** every advertised capability is either wired into `rollopsd` or
removed from docs/UI. Silent no-ops are bugs.

#### A1. Shared engine boot

Extract the daemon's engine option assembly (`WithAudit`, `WithGuardrails`,
`WithSecrets`, `WithGovernance`, `WithArtifactGate`, `WithNotifier`,
confinement, lease owner) into `internal/boot` (or equivalent) used by
**both** `cmd/rollopsd` and the one-shot CLI. One-shot today is a weaker
engine — that is a policy bypass.

Acceptance: `cmd/rollops` apply with a freeze engaged in the same SQLite file
is refused. Tests construct boot options from a fake env.

#### A2. Persist and display the real risk score

`Engine.Apply` already calls `EvaluateRisk` but **never writes**
`Rollout.RiskScore`. Reconcile/CLI/API pass empty `RiskInputs`.

- Write `d.Score` onto the rollout at apply time.
- Callers fill `RiskInputs`: env from `spec.target.env`, change-type from the
  plan (config vs code vs schema — start with config/code from whether the
  desired checksum changed vs a `database.migrate` hook), blast radius from
  `depgraph` when the caller has deps (reconcile: 0 is honest if no graph).
- UI: show that number. Drop the "decisionkit" label unless we call
  decisionkit. Heuristic High/Medium/Low remains only as a fallback when
  score is 0 **and** the gate was off (`threshold == 0` and no `sensitive`).

Acceptance: a prod + schema + canary apply stores a non-zero `RiskScore`;
dashboard tooltip does not say decisionkit.

#### A3. Wire metric analysis in the daemon

`WithMetricAnalysis` / `WithMetricsProvider` exist; `cmd/rollopsd` never
sets them. YAML `spec.analysis` is captured on the rollout and ignored.

- If a config (or env `ROLLOPS_ANALYSIS=1`) requests analysis, the daemon
  enables the engine flag and the Prometheus provider (plugin builder already
  exists for `metricprovider`).
- Default remains off (constraint `observability-free-v1`).
- Log at startup, same pattern as governance.

Acceptance: a unit test of daemon option assembly; an engine test already
covers the gate. A config with `spec.analysis` and the flag on **fails
promote** when the provider breaches.

#### A4. Wire Vault

`VaultProvider` and `Chain` exist; daemon uses `EnvProvider` only.

- `secrets.FromEnv`: if `VAULT_ADDR` is set, `Chain{Vault, Env}`; else Env.
- Token from `VAULT_TOKEN` or a file (`VAULT_TOKEN_FILE`), never logged.
- Startup log when Vault is in the chain.

Acceptance: live integration test already covers Vault resolve; add a
constructor test. Daemon with `VAULT_ADDR` unset is unchanged.

#### A5. Persist freeze

`security.Freeze` is in-memory. A restart silently lifts the kill-switch.

- Store a single freeze row (or key) in SQLite: active, reason, by, at.
- Daemon loads it into `Freeze` at boot. `Engage`/`Lift` persist.
- One-shot CLI sees it via the same Store (A1).

Acceptance: engage, close engine, reopen Store, apply is blocked. Agents
cannot Lift (existing RBAC).

#### A6. gRPC CLI is not plaintext-only

`internal/grpcapi/client.go` dials `insecure`. Daemon can require TLS.

Acceptance: with `ROLLOPS_TLS_*` set, `ROLLOPS_DAEMON=host:port` CLI uses
the same `servertls` material. Loopback plaintext remains the dev default.

#### A7. Traffic `SetWeight` fails the step

`driveTraffic` audits failures and continues. A canary that did not shift
traffic still bakes and promotes.

Acceptance: a router error fails `Apply` / the current step and can
auto-rollback. Feature-flag `apply_flag` stays best-effort (app-layer,
audited) — different failure domain.

#### A8. Image automation: `current` cannot mean stuck (#98)

v0.29/v0.30 made verdicts (`proposed`/`pending`) and logged identities.
The 18h stall is unexplained; we do not need the heisenbug to make the
verdict honest.

- `current` is **only** legal when the scanner identity equals the
  Git-pinned identity. Both are in the status line (already started in v0.30).
- The "file already contains this ref" path is a distinct outcome
  (`pinned` or equivalent) — never `current`.
- If the scanner offers a digest Git does not pin, the outcome cannot be
  `current` even if patch reports no change (that combination is `error`).
- Regression test: scanner returns digest B, file pins digest A → not `current`.

Acceptance: issue #98 can close on the invariant even if the original
stall is not reproduced. A later occurrence is visible on the first tick.

#### A9. Honesty docs (last in A)

After the wires: README, TDD §3/§10/§12/§16/§17, wiki/architecture,
vision status line, UI copy, `internal/store` package comment, MCP package
comment. Strike Postgres/mnemos/host-agent/standalone-MCP. Webhook is
Phase D — say "poll today; webhook in make-it-real" until D lands, then
update once. Vision "Concept / pre-MVP" → current (dogfooded OSS).

---

### Phase B — Agent-native dogfood

**Goal:** the USP is a production surface, not a README line.

#### B1. Deploy-agent RBAC role

Default `agent:*` is plan+status only (correct). Add a documented
`agent:deploy` role (plan/apply/rollback/status/verify — **not** freeze
lift, **not** approve) as an opt-in grant in `docs/security-rbac.md` and
an example policy snippet. Do not widen the default.

#### B2. MCP tools for observe

Add `rollouts.list`, `rollouts.history`, `rollouts.drift` (read, same
authz as status). Keep apply gated on plan-before-apply.

#### B3. Runbook

`docs/mcp-tokens.md` (exists) plus a short **agent operator** page:
enable `ROLLOPS_MCP_ADDR`, tokens file, policy grant, example tool flow
`plan → status → apply → verify → rollback`. Cluster dogfood is operator
work (set the env on the Deployment) — this task is the code+docs that
make that a 15-minute change, not a research project.

---

### Phase C — Canary as a verb

**Goal:** progressive delivery is controllable, or named honestly.

Today `Apply` uses `progressive.Executor` (blocking `time.Sleep`).
`Stepper` (pause/resume/promote/abort, snapshot) is unit-tested and
**unused**. Lifecycle has `EventPause` and never sends it.

#### C1. Tick-driven stepper

- `Apply` deploys once, starts a `Stepper`, persists snapshot + step
  progress on the rollout, returns while phase is `deploying` or `paused`.
- Reconcile tick (and `VerifyOrRollback`) advances the machine: health
  check, optional `SetWeight`, then wait or complete.
- Crash recovery: restore snapshot from Store (statekit already can).
- Hold the target lock across ticks via the existing lease, or re-acquire
  per tick keyed by rollout id — pick the lease; document it.

Acceptance: a canary with `pause: 50ms` in tests completes across two
`Tick` calls, not one blocking Apply. Restart mid-pause resumes.

#### C2. Pause / resume / abort on every surface

Engine methods + CLI + HTTP + gRPC + MCP + UI. Authorize as apply
(pause/resume/abort) scoped to the target ref. Abort triggers rollback.
UI never force-promotes (existing rule).

Acceptance: table test per surface; Playwright or UI bundle assertion
for the three buttons on an in-flight canary.

#### C3. Honest strategy names

- `doctor` / `plan`: if `strategy.type: canary` and no `trafficRouting`
  and no `featureFlags`, summary says **health-gated bake**, not traffic
  split.
- `strategy.type: blue-green` without traffic routing is a **full
  cutover**; say so. Real two-stack blue-green is out of scope.

---

### Phase D — GitOps wedge

**Goal:** the minimum to steal a 5–20 app Argo install without
ApplicationSets. RFC `docs/design/multi-cluster-scale.md` Phase 1 only.

#### D1. Git webhook listener

HMAC verify already exists (`internal/git/webhook.go`). Daemon has no
route.

- `POST /v1/hooks/github` (or `/hooks/git`), HMAC, then `watcher.Tick`
  for the matching repo (or all, if payload repo is unknown — still
  bounded by watch list).
- Secret from `ROLLOPS_WEBHOOK_SECRET`. Unset → route 404 (no open
  unauthenticated tick).
- Poll remains the safety net.

Acceptance: unit test with `git.Sign`; daemon test that a valid POST
calls Tick, invalid signature is 401 and does not Tick.

#### D2. `RolloutSet` list generator

In-memory expansion at watch load time. Git holds the template.
Generated configs are ordinary `RolloutConfig`s with deterministic
`target.ref`. No engine/store change.

```yaml
kind: RolloutSet
metadata: { name: web }
generators:
  - list:
      elements:
        - { name: east, kubeconfig: /etc/rollops/east, context: east }
        - { name: west, kubeconfig: /etc/rollops/west, context: west }
template:
  spec:
    target:
      kind: kubernetes
      ref: "web@{{name}}"
      spec:
        kubeconfig: "{{kubeconfig}}"
        context: "{{context}}"
        namespace: web
        resource: deployment/web
```

Acceptance: two elements → two validated configs, unique refs, each
reconciles independently. Cluster generator / matrix = later, not here.

#### D3. `ignoreDifferences`

Kubernetes target: list of json-pointers (or field paths) ignored in
Observe/Diff so HPA replicas, webhook defaults, etc. do not flap drift.
Apply stays client-side `kubectl apply` (SSA is a later decision).

Acceptance: live or fake-kubectl test: ignored field differs → not
drift; other field differs → drift.

#### D4. `dependsOn` waits in reconcile

DAG already feeds risk blast radius. Reconcile applies a config even if
its `dependsOn` target is not promoted.

Acceptance: B `dependsOn` A; A not promoted → B skipped this tick
(logged, not fatal to the rest of the repo). A promoted → B applies.

---

## Execution order (Roady)

See `.roady/spec.yaml`. Generate + approve after this RFC lands. Do not
start Phase B until A9 (honesty docs) is done — otherwise the agent
runbook will document a product that still lies. C1 is the largest
engine change; keep it atomic and behind tests. D2 is independent of C.

WIP: policy `max_wip: 3`. Prefer finishing a phase over opening the next.

## Done when

1. `roady drift detect` is clean for the new features (tasks verified).
2. README/TDD/UI match `rollopsd` behavior.
3. An agent with `agent:deploy` can plan/apply/verify/rollback through MCP
   against a non-prod target (docs + defaults; cluster enable is operator).
4. A canary can be paused and aborted from CLI and UI.
5. A GitHub webhook can trigger reconcile; a `RolloutSet` list expands.
6. Issue #98 closed on the `current` invariant.

Not done when: we have more Argo checkboxes. Stop.
