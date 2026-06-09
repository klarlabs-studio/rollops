# Status — Rolloffs

*Updated: 2026-06-08*

## 🚀 MVP REACHED — enforced pipeline wired + dogfood proven live

Phase-0 MVP done. The previously à-la-carte P0 capabilities are now composed into
ONE enforced deploy path (`engine.Apply`): guardrails (freeze/rate-limit/policy
floor) → risk gate → secret resolution (`secret:<ref>`) → artifact verify (cosign)
→ lifecycle gate → progressive deploy (apply once, health-gated per step) → audit
throughout. Reconciler finalizes via VerifyOrRollback (promote/auto-rollback).
`rolloffsd` constructs the full engine (audit+guardrails+secrets, +cosign opt-in).

**Dogfood verified LIVE on minikube**: git repo with `rolloffs.yaml` → rolloffsd
watches → deploys Deployment to minikube → `kubectl delete` (drift) → reconciled
within one tick → promoted; every phase audited (deploying→verifying→promoted),
attributed to ci/reconciler. Two real rollouts (deploy + drift-reconcile) in the
bolt trail. Example: `examples/kubernetes-rollout.example.yaml`.

205 tests, hermetic + -race green.

**Phase-1/2 features added post-MVP**: richer UI (drift/history/live/auto-refresh),
notifications (Telegram + webhook), and generic provider-agnostic metric-based
analysis (CEL condition over a MetricsProvider; Prometheus impl) wired as the 4th
post-deploy signal. **Six external integrations live-verified**: SSH, FTP, K8s,
Vault, cosign, Prometheus — plus the git→cluster dogfood. `make integration`.

## 🎉 P0 OSS core COMPLETE — 37/37 Roady tasks done

Full autonomous "until the end" run finished, **merged to `main`** (merge commit
d12e613, `feat/config-schema` deleted). `go build`/`go vet`/`go test ./...` all
clean. **169 test functions**, 31 packages. Both binaries build and run.

What's implemented (all TDD, every task committed atomically):
- **Foundation**: config (YAML+schema+CEL via internal/condition), SQLite store, target+Store contracts.
- **Engine**: 7-op surface (plan/apply/verify/promote/rollback/observe/schedule) + Approve/Reject + Validate + FireDueSchedules; statekit lifecycle; fortify step envelope; per-target locks; structured plan/diff.
- **Risk**: blast-radius 5-signal gate + CEL sensitive override; dependency DAG (cycle detect + blast radius).
- **Targets**: SSH + FTP (dumb, stamped) + Kubernetes (rich, kubectl, no client-go) + gRPC plugin protocol; shared conformance suite.
- **Delivery**: progressive rolling/canary/blue-green; auto-rollback (health+smoke+step); env promotion.
- **Trust**: secrets (Env/Vault + redacting Secret), bolt audit + redaction, RBAC, guardrails (policy floor + freeze + agent rate-limit), cosign artifact verification, git webhook HMAC + clone/pull.
- **Interfaces**: CLI (cmd/rolloffs), daemon HTTP/JSON API + RBAC (cmd/rolloffsd), MCP agent server (tools 1:1), Prometheus self-observability, read-and-act UI dashboard.

### Follow-ups — status
- ✅ **Reconciler git-watch loop** — DONE (cb2cd58): `reconcile.Watcher` clones N repos, pulls + reconciles each per tick; `rolloffsd` watches `ROLLOFFS_WATCH` repos. Real-git tested.
- ✅ **/ui auth** — DONE: dashboard behind Basic auth (`ROLLOFFS_UI_USER/PASSWORD`), refused if no password set.
- ✅ **Native gRPC** — DONE: `proto/rolloffs/v1` + generated stubs; `internal/grpcapi` server (auth interceptor + RBAC) + client adapter (cli.Operations); `rolloffsd` serves gRPC, CLI daemon mode via `ROLLOFFS_DAEMON`. bufconn-tested + verified live end-to-end. (grpc-gateway REST codegen still optional — REST already served by `internal/api`; gateway plugin not installed.)
- ⬜ **axi-go capability modeling** of each step — deferred by design; fortify envelope (internal/step) delivers resilience now.
- ✅ **Integration tests (SSH + FTP)** — DONE: docker-compose harness (real sshd + vsftpd) behind `integration` tag, `make integration` / `test/integration/run.sh`. SSH passes the full conformance suite live; FTP deploy/observe round-trips. **Found + fixed a real bug**: SSH transport `Run` shared one bytes.Buffer for stdout+stderr → data race (x/crypto/ssh copies them concurrently) → empty Observe after successful Apply. ✅ **Kubernetes** integration too (`make integration-k8s` against minikube) — rich
target verified live: kubectl apply + checksum annotation, live-cluster Observe,
idempotent re-apply, rollout health. (Note: minikube needed `--apiserver-names=minikube`
to dodge the empty-certSAN bug #9175.) **All three first-party targets now have
live integration coverage.**
✅ **Vault + cosign** live too: Vault dev server (KV v2 resolve + Secret redaction)
and a real cosign-signed image in a local registry (verify pass, unsigned reject)
— CosignVerifier gained `--key` + `--allow-http-registry`. **Every external
integration (SSH/FTP/K8s/Vault/cosign) is now verified against real infra.**
`make integration` runs all five (K8s/Vault/cosign skip gracefully if their infra
is absent).

### P1/P2 (out of original P0 scope)
Historical-failure risk signal, DB rollback, notifications (Telegram), metric-based analysis (Obvia seam), multi-instance coordination, managed multi-customer studio layer.

---
*(historical entries below)*

## Where we are

Greenfield scaffold complete. Module `go.klarlabs.de/rolloffs` (Go 1.26),
git on `main`, domain skeleton in place, builds clean (`go build ./...` OK).
All 7 stack deps pinned + resolving. Roady plan **ordered & approved: 37 tasks, 2 done, 35 pending**, dependency-gated.

**19/37 tasks done** (autonomous "until the end" run). Phases A–D + risk gate largely complete.

Done since last checkpoint: lifecycle (statekit), plandiff, locks, conformance suite, step-exec
(fortify), SSH/FTP/K8s targets (+Builtin registry), risk gate + approve/reject, dep DAG.
New packages: internal/{rollout(lifecycle), step, risk, depgraph}, internal/target/{ssh,ftp,kubernetes},
pkg/conformance. Engine now drives phases via statekit, wraps targets in fortify, gates on risk.
Remaining: config-perrepo, progressive, lifecycle-validate, git, reconcile, rollback(task),
schedule, env-promo, secrets, audit, security-rbac, artifact, guardrails, cli, daemon-api, mcp,
selfobs, ui, plugin-grpc. All green; `go vet ./...` clean.

---
**7/37 tasks done.** Phase A complete + engine keystone (`t-engine-api`) in. Phase B underway.

## Done this session

- git init + go.mod (`go.klarlabs.de/rolloffs`) + domain skeleton (cmd/, internal/, pkg/).
- Two core contracts building: `pkg/target.Target`, `store.Store` + `rollout` model.
- All 7 Klarlatz stack deps pinned + resolving (`internal/stack` anchor).
- Roady: spec (23 features, 9 constraints) + **37 dependency-ordered tasks**, approved.
- README, AGENTS.md, vision/TDD in-repo, Agent OS memory.
- **t-config-schema** (1d55d34): config types + embedded JSON schema + version-gated Parse.
- **t-config-validate** (d10cef4): `Validate`/`Load` — jsonschema + semantic rules, aggregated errors.
- **t-config-cel** (bc0f28a): `internal/condition` CEL evaluator, wired into validation.
- **t-store-sqlite** (f2762a4): pure-Go SQLite `store.Store` + migrations + crash-safe persist.
- **t-engine-api** (49d1c86): `internal/engine` 7-op surface + `internal/target.Registry`. Apply/Plan/Observe/Verify/Promote/Rollback/Schedule over Store+Registry; clock+idgen injectable.

All on branch `feat/config-schema` (5 commits) + scaffold on main (9848fa6). Branch unmerged, awaiting review.
Full suite green: config(13), condition(10), sqlite(8), engine(8), target(3). `go vet ./...` clean.

## Next (ready tasks)

1. **t-lifecycle** — statekit statechart formalizing the phase transitions the engine drives directly today (guards, durable transitions). Depends engine-api ✓ + store ✓.
2. **t-engine-plandiff** — rich plan/diff (current → desired field-level) beyond the checksum-changed bool.
3. **t-dep-dag** — dependency DAG (cycle detect, serialize chains); also feeds blast-radius.
4. **t-conformance** — target conformance suite (idempotency/fingerprint/health). Unblocks Phase D targets.
5. **t-engine-locks**, **t-config-perrepo** also ready.

See `roadmap.md` for full phased order.

## Note for next session
Engine drives phases directly (deploying→verifying / rolled-back). `t-lifecycle` (statekit)
will formalize transitions + guards; expect to refactor engine phase-setting to go through
statekit then. Not rework — planned layering.

## Blocked / open

- None blocking. Stack confirmed + pinned (see decisions/open-threads).
- Repo dir is `rollops`, project/module is `rolloffs` — intentional, noted in README.

## Competitive (vs Argo/Flux) — added 2026-06-09
- **Helm + Kustomize rendering** in the K8s target (helm template / kubectl kustomize, remote charts/overlays) — the biggest adoption gap closed. examples/helm-rollout.example.yaml.
- **UI: Sync-now button** (on-demand reconcile, wired to the watcher) + live phase pulse.
- Moat unchanged + stronger: infra-agnostic (non-K8s), agent-native (MCP), lean single binary, risk gate + guardrails + audit.
- Still open vs Argo/Flux: prune/GC, real `kubectl diff` view, resource tree, SSO, multi-cluster, image automation. Tracked for next.

## Competitive round 2 (2026-06-09)
- **Prune/GC** (k8s, opt-in spec.prune): label-inject + kubectl apply --prune -l; live-verified (configmap removed→pruned). examples + integration test.
- **Resource tree** (Deployment→Pods) via Inspector (matchLabels→pods); UI detail renders indented tree.
- **Rich UI detail page** (/ui/target): live diff (kubectl diff), live resource tree, rollback, history, sync. Differ/Inspector optional target capabilities; engine Diff/Resources/RollbackLast; promoted→rolled-back added to statechart.
- **Helm + Kustomize rendering** + **Sync-now** + **phase pulse** (earlier this session).
- Still open vs Argo: SSO, multi-cluster (deferred per user). 69 commits, 222 tests.
