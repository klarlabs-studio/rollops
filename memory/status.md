# Status — Rollops

*Updated: 2026-06-10*

## ✅ Hetzner k3s dogfood (2026-06-11, v0.5.0 binaries)

Full lifecycle verified LIVE on the klarlabs edge cluster (k3s v1.35.4,
edge-1 + edge-light, via Tailscale, context `felixgeelhaar`, isolated
namespace rollops-e2e, temp db): doctor → plan (create) → canary apply
with live step health gates (`steps 2/2 (100%)` + per-step timeline
notes) → promote → update (2→3 replicas) → rollback (verified live) →
out-of-band kubectl delete → drift flagged on promoted target → re-apply
healed. Daemon API served real k3s resource tree (Deployment→Pods across
both nodes). Namespace + temp files cleaned up.

Findings — RESOLVED 2026-06-11 (commit 49f40d7, unreleased):
1. ~~Drift baseline after rollback~~: DriftReport now asserts for both
   settled phases (promoted + rolled-back; rollback persists the restored
   manifest as Desired). Engine test covers match + tamper cases.
2. ~~UI banner honesty~~: three banners — unauthorized (401/403,
   immediate), stale-with-last-known-state, and nothing-loaded-yet (the
   "last known state is blank" case Felix spotted). Plus
   `Cache-Control: no-cache` on /ui so daemon upgrades never serve a
   stale cached bundle (third finding, discovered while fixing #2).

## 🏷️ v0.5.0 TAGGED — target plugin runtime

Plugin lifecycle released as v0.5.0 (GitHub release with archives).

## Plugin process lifecycle (2026-06-11, post-v0.4.0)

TDD §17 plugin future-work closed: `plugin` target kind launches sha256-
pinned plugin binaries as subprocesses (stdout handshake → unix-socket
gRPC → stdin-close teardown). Public authoring toolkit `pkg/plugin`
(Serve + handshake) with public proto stubs `pkg/plugin/rollopspluginv1`
(rollops.plugin.v1.TargetPlugin). Engine releases Closer targets after
every operation; step.Guarded forwards Close. E2E tested against real
compiled subprocess. Roady feature added + task completed, 118/118, no
drift. Studio decision: stays out of OSS repo (open-core boundary);
operator chose plugin lifecycle over starting studio repo.

## 🏷️ v0.4.0 TAGGED — live progressive step progress

Engine persists per-step progress (migration 0002), console step bar +
mini indicator, gRPC/CLI parity (`steps 2/4 (50%)` in status). Released
to GitHub with archives. All headline Argo Rollouts gaps closed.

## 🏷️ v0.3.0 TAGGED — console risk + agent attribution

UI competitive round 3 released as v0.3.0 (GitHub release with archives).

## UI competitive round 3 (2026-06-10, post-v0.2.0)

Review vs ArgoCD/Argo Rollouts closed remaining gaps and shipped two
differentiators no competitor has: real decisionkit risk scores surfaced
per app/rollout, and agent/human/CI actor attribution icons everywhere
(agent-first USP, visualised). Plus table stakes: relative timestamps,
clickable status facets, diff colouring, rollback modal (replaces native
confirm), "/" + Escape keyboard, tab-title attention count, hidden-tab
polling pause, stale banner, aria roles. Verified live with fixture
backend + Playwright: zero console errors, zero aria violations.
Progressive step visualization: DONE post-v0.3.0 (commit c1e8ebf,
unreleased). Engine persists per-step progress (OnStep hook + sqlite
migration 0002 step_index/total/weight), timeline note per step, console
step bar + apps-table mini indicator. Last headline Argo Rollouts gap
closed. Note: gRPC/CLI status does not yet carry step fields (proto
change) — minor follow-up if CLI parity wanted.

## 🏷️ v0.2.0 TAGGED — email notifications

Operator decision: email instead of Telegram. v0.2.0 ships notify channels
briefkasten (durable MCP outbox, preferred), direct SMTP, and the HMAC
webhook, plus the `rollops doctor` notify probe and docs/notifications.md.
Breaking: `ROLLOPS_TELEGRAM_*` removed. Released to GitHub with archives.

## 🏷️ v0.1.0 TAGGED — first release cut

The rename/productization worktree was committed as `8bb6094`
(`feat!: rename rolloffs to rollops and ship P2 productization`, 166 files)
and tagged **v0.1.0** (annotated tag). `make release-check` and
`make dist-check` green post-commit; archives
`dist/rollops_v0.1.0_{darwin_arm64,linux_amd64,linux_arm64}.tar.gz` verify
against `checksums.txt`; `bin/rollops version` reports `v0.1.0 (8bb6094)`.

**Published 2026-06-10**: public repo `github.com/klarlabs-studio/rollops`
(main + tag pushed), GitHub release v0.1.0 with the three archives +
checksums, and vanity import `go.klarlabs.de/rollops` (go-vanity commit
`fe6447b`, deployed to edge cluster via configmap update + rollout restart).
Verified end-to-end: `GOPROXY=direct go list -m go.klarlabs.de/rollops@v0.1.0`
resolves. Note: go-vanity deploy emits a pre-existing PodSecurity
"restricted" warning for the nginx container (allowPrivilegeEscalation /
capabilities / seccompProfile unset in k8s/manifests.yaml).
Next: P3/studio scope or notification channel (Telegram, P1).

## Roadmap COMPLETE — near roadmap + P2 verified

Roady is fully closed: **117/117 tasks verified**, no pending/in-progress/done
tasks, and `roady drift detect` reports no drift. Final verification anchor:
`make release-check` passed on 2026-06-10 after the P2 work.

P2 delivered in this pass:
- Stable optional metric analysis with Prometheus/CEL config validation and UI
  history notes.
- Historical rollback risk signal with configurable lookback/weight and CEL
  variables.
- Database rollback command hook for auto-rollback, surfaced in history/CLI/UI.
- SQLite-backed runtime leases for shared target locks and reconcile leader
  election.
- OIDC-style bearer auth for API/UI plus external group-to-RBAC mapping.
- Image update policy and Git YAML writeback helpers.
- Studio boundary and fleet dashboard contracts.
- Optional feature flag and governance hook contracts.

## 🚀 MVP REACHED — enforced pipeline wired + dogfood proven live

Phase-0 MVP done. The previously à-la-carte P0 capabilities are now composed into
ONE enforced deploy path (`engine.Apply`): guardrails (freeze/rate-limit/policy
floor) → risk gate → secret resolution (`secret:<ref>`) → artifact verify (cosign)
→ lifecycle gate → progressive deploy (apply once, health-gated per step) → audit
throughout. Reconciler finalizes via VerifyOrRollback (promote/auto-rollback).
`rollopsd` constructs the full engine (audit+guardrails+secrets, +cosign opt-in).

**Dogfood verified LIVE on minikube**: git repo with `rollops.yaml` → rollopsd
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

## 🎉 P0 OSS core COMPLETE — Roady verified and drift-free

Roady tracker normalized on 2026-06-09: generated spec-coverage tasks now map
1:1 to the 63 published requirements and all 100 plan tasks are verified. Drift
check is clean (`roady drift detect`: no drift detected). Product validation is
anchored by `make release-check`.

## Historical P0 checkpoint — 37/37 curated tasks done

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
- **Interfaces**: CLI (cmd/rollops), daemon HTTP/JSON API + RBAC (cmd/rollopsd), MCP agent server (tools 1:1), Prometheus self-observability, read-and-act UI dashboard.

### Follow-ups — status
- ✅ **Reconciler git-watch loop** — DONE (cb2cd58): `reconcile.Watcher` clones N repos, pulls + reconciles each per tick; `rollopsd` watches `ROLLOPS_WATCH` repos. Real-git tested.
- ✅ **/ui auth** — DONE: dashboard behind Basic auth (`ROLLOPS_UI_USER/PASSWORD`), refused if no password set.
- ✅ **Native gRPC** — DONE: `proto/rollops/v1` + generated stubs; `internal/grpcapi` server (auth interceptor + RBAC) + client adapter (cli.Operations); `rollopsd` serves gRPC, CLI daemon mode via `ROLLOPS_DAEMON`. bufconn-tested + verified live end-to-end. (grpc-gateway REST codegen still optional — REST already served by `internal/api`; gateway plugin not installed.)
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

Greenfield scaffold complete. Module `go.klarlabs.de/rollops` (Go 1.26),
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

- git init + go.mod (`go.klarlabs.de/rollops`) + domain skeleton (cmd/, internal/, pkg/).
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
- Repo dir is `rollops`, project/module is `rollops` — intentional, noted in README.

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

## Productization pass (2026-06-09)
- Build/dev polish: `fmt`/`fmt-check` now ignore deleted legacy Go paths, so
  `make all` is quiet after the Rollops rename.
- UI pipeline is explicit: `make ui-typecheck` and `make ui-build` cover the
  TypeScript console and regenerate the embedded bundle.
- Git hygiene: root `rollops`/`rollopsd` binaries and local `node_modules/` are
  ignored; the stray root npm lockfile was removed.
- Verification: `make fmt-check`, `make ui-typecheck`, `make ui-build`, and
  `make all` pass. In this sandbox, Go still logs non-fatal stat-cache write
  denials for the user-level module cache during `go build`; binaries are built.

## Productization pass 2 (2026-06-09)
- UI dashboard now has a fast client-side filter across target refs, phases,
  desired/observed fingerprints, rollout IDs, strategies, and actors. This keeps
  the console usable as dogfood grows beyond a handful of services without
  adding server query semantics yet.
- Filtered empty states distinguish "nothing exists" from "no match", so the
  dashboard does not look broken when an operator narrows too far.
- Embedded UI asset test now catches stale bundles by asserting the filter CSS
  and compiled placeholder are served.
- Verification: `make ui-typecheck`, `make ui-build`, `go test ./internal/ui`,
  and `make all` pass.

## Productization pass 3 (2026-06-09)
- CLI now exposes `rollback <target-ref>` over the same `Operations` seam as
  plan/apply/status/promote, so one-shot operators can recover without opening
  the UI or MCP.
- gRPC daemon mode now exposes `Rollback`, authorized through
  `rollouts.rollback` against the target ref, and the CLI client adapter drives
  it via `RollbackLast`.
- README quickstart advertises rollback as a first-class command.
- Verification: `make proto`, `go test ./internal/cli`,
  `go test ./internal/grpcapi`, and `make all` pass.

## Naming cleanup (2026-06-09)
- Rollops is the canonical product title everywhere in the current tree.
  Remaining legacy pre-Rollops text/path references were removed from working
  files, including the SQLite migration comment and status memory.
- Verification: repository-wide legacy-name search returns no working-tree
  matches; `make all` passes.

## Productization pass 4 (2026-06-09)
- HTTP/JSON API now exposes `POST /v1/rollback` with body
  `{"target":"<target-ref>"}`, matching the recovery capability already present
  in CLI, gRPC, MCP, and UI.
- Rollback is authorized through `rollouts.rollback` scoped to the target ref;
  bad input returns 400, unauthorized viewers get 403, and no-prior rollback
  failures return 412.
- README quickstart lists rollback in the daemon HTTP surface.
- Verification: `go test ./internal/api` passes.

## Release polish 1 (2026-06-09)
- CLI now exposes `rollops doctor [config.yaml]` as a first-run diagnostic.
  Local mode validates an optional config and opens/migrates the configured
  SQLite DB. Daemon mode validates an optional config and probes gRPC
  reachability/auth via `ROLLOPS_DAEMON` + `ROLLOPS_TOKEN`.
- `cmd/rollops` routes `doctor` before opening the normal engine path, so a
  broken local DB can be diagnosed instead of aborting command startup.
- README quickstart shows `doctor` for one-shot and daemon modes.
- Verification: `go test ./internal/cli ./cmd/rollops`, `make all`, and a
  smoke run of `bin/rollops doctor examples/rollout-config.example.yaml` pass.

## Release polish 2 (2026-06-09)
- Added systemd packaging for the lean VPS path: `deploy/systemd/rollopsd.service`,
  `deploy/systemd/rollopsd.env.example`, `scripts/install-systemd.sh`, and
  `docs/deploy-systemd.md`.
- The unit runs `rollopsd` as a dedicated `rollops` user, stores SQLite/runtime
  state in `/var/lib/rollops`, reads `/etc/rollops/rollopsd.env`, binds HTTP/gRPC
  to loopback by default, and applies baseline systemd hardening while keeping
  native target transports usable.
- `make package-check` validates the install helper with `bash -n` and
  `shellcheck`; `make install-systemd` builds and delegates to the installer.
- README now links the VPS/systemd deployment path.
- Verification: `make package-check`, `make all`, and
  `scripts/install-systemd.sh --help` pass.

## Release polish 3 (2026-06-09)
- All shipped rollout examples are now product surface, not loose samples:
  `internal/config` loads every `examples/*.yaml` through schema + semantic
  validation, and `make examples-check` runs that focused guard.
- Added `docs/first-run.md` with the shortest local path: build, doctor, plan,
  daemon mode, Git watch, examples validation, and VPS handoff.
- README now links the first-run guide and advertises `make examples-check`.

## Release polish 4 (2026-06-09)
- RBAC bootstrap defaults moved into `security.DefaultRBACPolicy()` so daemon
  authorization policy is tested in the security package instead of assembled
  inline in `cmd/rollopsd`.
- Bootstrap roles are now explicit: `human:admin` can plan/apply/approve/
  rollback/status/schedule/freeze; `agent:*` can plan/status only until an
  operator adds narrower deploy grants.
- Added `docs/security-rbac.md` documenting identities, permissions, scopes,
  bootstrap defaults, and the recommended first VPS policy.

## Release polish 5 (2026-06-09)
- Plugin target adapter now fails closed on a nil RPC connection instead of
  panicking, and rejects `HealthUnknown` or out-of-range health states before
  they reach the engine.
- Added tests for nil-RPC handling and invalid health-state rejection alongside
  the existing plugin conformance test.
- Added `docs/target-plugins.md` documenting protocol version/cookie,
  required target semantics, concrete health states, secret hygiene, rollback
  behavior, and the conformance test pattern.

## Release polish 6 (2026-06-09)
- UI dashboard now surfaces an operator attention queue above the full tables:
  awaiting approvals first, drifted targets second, and active rollouts third.
- Attention rows reuse existing actions (`Approve`, `Reject`, `Sync`, `Open`)
  and are computed entirely client-side from the existing dashboard API.
- Embedded UI tests now assert the shipped bundle includes the attention queue.

## Release polish 7 (2026-06-09)
- Added `make release-check` as the local OSS release gate, aggregating UI
  typecheck/build, example validation, package helper validation, formatting,
  vet, tests, and binary builds.
- Added `docs/release-checklist.md` with local, optional live, and manual smoke
  checks before tagging.

## Release polish 8 (2026-06-09)
- Added build metadata via `internal/version` and Makefile `-ldflags`.
  `rollops version` and `rollopsd version` now report version, commit, and build
  date.
- Added `CHANGELOG.md` for the initial v0.1.0 MVP OSS core release.

## Release polish 9 (2026-06-09)
- Roady tracker state normalized after productization: `roady plan generate`
  added the 63 requirement-title coverage tasks expected by drift detection,
  while preserving the 37 curated implementation tasks.
- All generated requirement coverage tasks were moved through start, complete,
  and verify with evidence pointing at `make release-check` and this session
  capture.
- Verification: `roady drift detect` reports no drift; `roady status` reports
  100/100 verified tasks and 100% progress.

## Near roadmap closed (2026-06-09)
- Argo-like operator UI pass is implemented. Dashboard now has a dense
  application list with health, sync/drift, derived operational risk, phase,
  desired/observed fingerprints, and last actor.
- Target detail now has desired-from-Git, observed-live, runtime-state, and
  operator-action summary cards, plus resource graph/list, diff, and rollout
  timeline.
- Responsive smoke verified desktop and mobile; mobile no longer has page-level
  horizontal overflow.
- Verification: `make release-check` passes; `roady drift detect` reports no
  drift.

## Release cut prep (2026-06-09)
- Added `make dist` / `make dist-check` to generate cross-platform release
  archives for `linux/amd64`, `linux/arm64`, and `darwin/arm64`, each containing
  both binaries plus README, changelog, docs, examples, systemd assets, and the
  install helper.
- `dist/checksums.txt` is generated and verified with `shasum -a 256 -c`.
- Verification: `make dist-check`, `make release-check`, and
  `roady drift detect` pass.
