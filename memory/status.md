# Status — Rollops

*Updated: 2026-08-14*

## Current State

Rollops is **released at v0.30.0** (tree HEAD; cluster may lag — last recorded
in-cluster pin was v0.26.0). It operates the creator's GitOps fleet. The
**near-roadmap is Make it real** (`docs/design/make-it-real.md`): wire advertised
behaviour, then dogfood MCP, then controllable canaries, then a small GitOps
wedge. Do not add Argo checkboxes outside that RFC.

**Last Session Summary (2026-08-14):** Product review, then a plan to make the
gaps real. Encoded as Roady features `real-trust` / `real-agent` /
`real-canary` / `real-gitops`. Phase A is **complete in Roady** (trust pass):
freeze persists, shared `internal/boot`, risk score on the rollout, Vault+Env,
`ROLLOPS_ANALYSIS` opt-in, callers fill `RiskInputs`, traffic fail-closed,
image-auto `current` iff scanner==Git pin, CLI policy parity, gRPC TLS client,
honesty docs. 134/142 tasks verified. Phase B (agent dogfood) is closed:
opt-in `agent-deploy`, MCP list/history/drift, operator runbook. Next is
Phase C (canary as a verb) — stepper, pause/resume/abort, honest strategy
names.

---

## Historical — 2026-07-19

Rollops is **released at v0.26.0** and operating its creator's entire production
cluster (GitOps fleet, one in-cluster `rollopsd`, ns `rollops-system`, watching 11
repos). **The v0.20-era ghcr image-push blockage is RESOLVED** — `rollopsd:v0.26.0`
published cleanly (goreleaser binaries + GitHub Release + Docker image all green),
and the **cluster is running v0.26.0** (pod Running/Ready, 0 restarts). Headline of
this release: **`manifestFrom`** — Kubernetes targets can now reference manifests
(path / kustomize / helm) instead of inlining them, plus a rollback that restores
the exact deployed rendered bytes.

**Last Session Summary (2026-07-17):** Shipped the #57 feedback end-to-end.
**#58 `manifestFrom`** — referenced manifest sources rendered at plan/apply time,
relative to the config-file dir; non-breaking vs inline/flat keys; drift keyed off
the rendered output; path-confined; `plan`/`doctor` show the rendered result. Kept
the lean posture (shells out to `kubectl kustomize`/`helm template`, no `client-go`).
**#59** — rollback restores captured `pt.Manifest.Rendered` bytes instead of
re-rendering (root-independent across CLI/UI/MCP/API/gRPC; also fixes the latent
daemon "referenced files changed since deploy" case). Released **v0.26.0** (backfilled
the missing v0.25.0 changelog; #61 admin-merged past the docs-PR CI path-skip),
rolled the cluster to v0.26.0, and synced the in-repo deploy manifest (#62). Full
detail in sessions/2026-07-17.md.

**Last Session Summary (2026-07-18):** Closed the #65 follow-up — **#72**, manual
`Verify`/`Promote` now run the **smoke gate**, not just metric analysis. Smoke
config is captured on the rollout at deploy time (migration **0009**, mirroring
0008/analysis) and the manual paths gate in the auto path's order (health → smoke
→ analysis); no-op when unconfigured, fails closed on an unreadable descriptor,
auto path unchanged (still exactly one smoke run). Merged, CI green, cluster NOT
yet rolled. Two threads opened in the process: **`Engine.Verify` has no production
callers** (remove it, or expose a real `verify` dry-run verb — every operator
surface calls `Promote`), and the **CHANGELOG is unwritten for #65/#66/#72**.

**Last Session Summary (2026-07-18):** Four PRs merged (#73 memory, #74, #75, #76),
`main` green. Started from the #65 follow-up and pulled a thread: **#72** brought
the smoke gate to manual Verify/Promote; **#74** made `verify` a real dry-run verb
(per-gate `VerifyReport`, changes nothing) and made `promote` enforce exactly what
it dry-runs, with an audited `--force` — both paths now share ONE gate runner, the
structural fix for the drift. Auditing a forced promote surfaced that promotion was
never audited at all. Asking whether MCP tokens belonged in a file rather than an
env var surfaced a **real security bug (#75)**: smoke tests and DB hooks — commands
named by untrusted repo config — inherited the daemon's whole environment, so a
watched repo could read every daemon secret. The plugin host had been hardened
against exactly this; the smoke path never was. Both now share one implementation.
**#76** then moved MCP tokens to a file with SIGHUP rotation. #74/#75/#76 were each
verified end-to-end against real infrastructure (SSH target via Docker; a live
rollopsd for token rotation), not just unit tests.

**Also 2026-07-18 (later):** cut **v0.27.0** (GitHub release + ghcr image, both
jobs green; cluster deliberately left on v0.26.0 since rollopsd is applied by
hand). Then **executed the git-auth migration end to end** — the whole fleet now
authenticates with GitHub Apps instead of the classic PAT, verified live in the
daemon logs. Two Apps already existed (created earlier that morning); the work was
fixing their install scope, loading the keys, and cutting over. Both Apps' scopes
were missing watched repos — that check is the load-bearing step, see
open-threads. armada was dropped from the watch list on operator instruction.

**Credential topology audited (2026-07-18):** after the git-auth migration, swept
124 repos + the cluster to find what still depends on the 4 remaining classic
PATs. Only `pet-medical-www packages read` is unreferenced; the rest are
load-bearing. Headline risk is **`k3s-ghcr-pull`, expiring 2026-08-29** — one
credential shared by all ten `ghcr-pull` Secrets, 39 pods, fleet-wide
`ImagePullBackOff` when it lapses. Detail + verification recipes in open-threads.

**Last Session Summary (2026-07-19):** Started as rollops housekeeping and ended in
nox. Two tasks: clean the drift in `open-threads.md`, and fix the 28 repos whose
`.nox.yaml` excludes `go.sum`. **The second task was based on a wrong note in this
very memory and would have made things worse.** Measured instead of trusting:
excluding `go.sum` is CORRECT — it hashes the whole module graph, so ~99% of its
findings name versions the build never selects (148/148 stale on mnemos). Opening
those 28 PRs would have injected ~5,263 false positives fleet-wide. The real bug
was in nox, and pulling it surfaced two more: **every dependency finding ever
produced was `medium` with an empty summary** (5,263/5,263), so a critical
dependency CVE could never trip the high/critical gate — and the shipped
fix-version remediation field had never emitted anything, because both read data
OSV's `querybatch` does not return. Third: advisories matched per module, not per
affected import path. All three fixed in **Nox-HQ/nox#248** (merged `2c3888c`),
released as **nox v1.10.0**, and the shared `go-ci.yml` pinned **1.7.1 → 1.10.0**
(klarlabs-studio/.github#38). Canary before merging the pin: **agent-go 56
gate-failing findings (42 critical)**, senat-os 3, mnemos 2, seven others 0 — those
vulnerabilities were always there, mislabelled medium. Also retracted a claim made
earlier in this file: rollops does NOT have a live `GO-2026-5932`; it is scoped to
`x/crypto/openpgp`, which rollops does not import. Full detail in
sessions/2026-07-19.md.

**Next Session Should:** **Triage agent-go's 42 critical dependency
vulnerabilities** — that is the real finding of 2026-07-19 and the only genuinely
urgent item; everything else that session was plumbing to make it visible. Then
drop the `go.sum` excludes across the remaining 27 repos (now unblocked by
v1.10.0 — they were correct workarounds, not bugs, and must NOT be removed on any
repo still pinned below 1.10.0), leaving agent-go last so its criticals surface
deliberately rather than inside a bulk sweep. Lower priority: relicta's `notes`
(silent exit 1, `model: gpt-4` in `.relicta.yaml`) and `approve` (renders `0.0.0` /
`0 commits`) are both broken in the nox repo — the v1.10.0 changelog had to be
hand-written; fix before the next cut.

**Superseded (2026-07-18 plan, kept for context):** Most open threads closed —
housekeeping
(flat-key decision, docs-PR merge gotcha #63, PAT revocation) plus two features:
metric analysis on manual `Verify`/`Promote` (#65) and **MCP per-caller bearer auth
(#66, fail-closed, BREAKING)**. FIRST: **before the next MCP-serving deploy, set
`ROLLOPS_MCP_TOKENS` and give every MCP caller a bearer token** — #66 is merged but
undeployed (cluster still v0.26.0); deploying without tokens dark-fails the MCP
surface. Remaining backlog, each its own effort: (1) **roady #34** — least-privilege
multi-org git auth (deploy keys / GitHub App) to replace the classic PAT — DESIGN
first with the operator; (2) marketing-sites digest→semver flip (per-app `.rollops`,
gated on a semver release); (3) hermes/mnemos shared-service config ownership;
(4) small follow-up: manual `Verify` still skips the smoke test.

## 🛠️ Distribution maturity (2026-06-11, unreleased, → v0.7.0)

Release automation (commit 0f2af2d): .goreleaser.yaml + .github/workflows/
release.yml — tag push v* builds archives/checksums/GitHub release, rebuilds
+ staleness-checks the UI bundle. Replaces manual make dist/gh release.
Homebrew DEFERRED (brews deprecated → cask/quarantine needs own pass; go
install covers CLI). NOTE: HOMEBREW_TAP_GITHUB_TOKEN secret not needed (brew
dropped).
`rollops plugin install` + cosign signing (commit 4a773e7): fetch (path/https)
→ optional cosign verify-blob (key or keyless) → install to ~/.rollops/plugins
→ print sha256 pin. security.CosignBlobVerifier added. Trust chain: verify sig
at install, enforce sha256 pin at launch. Both items sitting unreleased.

CONVERSATION PENDING: operator wants to talk before picking Obvia vs Relicta
(the two remaining contract-only Phase-2 OSS seams). Other forward options:
Studio commercial repo, live-Flagsmith verification, plugin marketplace/registry,
path to 1.0.

## 🧩 Plugin architecture v2 + feature flags (2026-06-11, commit ab30c93, unreleased)

Phase-2 ecosystem work. Operator chose nox-hq's plugin model. Replaced the
typed TargetPlugin gRPC service with ONE generic Plugin service (GetManifest +
InvokeTool). Plugins declare capabilities (named tool groups) + safety
requirements (network/file/env/risk-class) in a manifest; host validates
against a safety Policy before invoking — capability-scoped trust. New kinds =
new capabilities, not new services. Structure: pkg/plugin SDK
(NewManifest/NewServer/Serve + ServeTarget + ServeFlagProvider, wire.go
payloads); internal/pluginhost (shared launch/handshake/manifest/policy/invoke/
teardown, extracted from old target launcher); internal/target/plugin thin
target-capability adapter. Feature-flag plugins = first new capability:
featureFlags spec block (plugin+sha256+flag+environment+when), engine drives
flag % per progressive step (OnStep) and/or 100% on promote (when:
step|promote|both), best-effort/audited. BREAKING vs v0.5.0 plugin protocol
(pre-1.0, no external plugins). E2E-tested with real subprocess plugins for
both capabilities. docs/feature-flags.md + example added. Roady 119/119, no
drift. Next: v0.6.0 cut. Remaining Phase-2 OSS seams: Obvia observability,
Relicta governance (both contract-only). Studio = separate commercial repo.

## 🔍 Expert multi-agent review (2026-06-11, commit f75eb33)

7-angle code review (line/removed/cross-file/reuse/simplify/efficiency/
altitude) + security pass on plugin runtime over v0.5.0..HEAD. Fixed:
UI authFailed stuck-flag (transient error after 401 mislabelled);
`paused` phase invisible in UI predicates (held canary → Unknown/no
progress/not in attention) — consolidated 4 duplicated phase regexes into
isActive/isDegraded helpers; plugin binary symlink-resolve before sha256
verify; docs/target-plugins.md Security model section.
Refuted (verified, no change): reconcile-loop from drift widening
(reconcile keys on Plan-vs-Git, not DriftReport); secret leak to plugins
(Apply uses unresolved manifest; plugin.Build never forwards resolved
tcfg.Spec); socket-redirect (plugin already host-user code).
Deferred hardening: ALL RESOLVED 2026-06-11 (commit f28e690). Phase.Settled/
Active/Degraded methods (engine uses Phase.Settled, free helper removed);
Store.ObservedFingerprints bulk read replaces per-target N calls in
DriftReport; plugin Setpgid process group + group-sweep on Close (graceful
AND timeout — orphan-free, verified by forked-child test) + line-tagged
1MiB-capped stderr writer + bounded handshake scanner. Unix-guarded with
no-op fallback. Note: reconcile-after-rollback semantics (Git wants forward
A, rollout.Desired=B after rollback → reconcile re-applies A every tick if
A stays unhealthy) is pre-existing and matches Argo/Flux retry behavior —
not a bug, left as-is.

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
