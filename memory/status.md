# Status — Rolloffs

*Updated: 2026-06-08*

## 🎉 P0 OSS core COMPLETE — 37/37 Roady tasks done

Full autonomous "until the end" run finished. `go build ./...`, `go vet ./...`,
`go test ./...` all clean. **167 test functions**, 31 packages, ~40 commits on
`feat/config-schema`. Both binaries build and run (`rolloffs`, `rolloffsd`).

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
- ⬜ **Native gRPC + grpc-gateway codegen** (protoc) — HTTP/JSON works today; gRPC is mechanical codegen, `internal/api` is the surface shape. Needs protoc toolchain.
- ⬜ **axi-go capability modeling** of each step — deferred by design; fortify envelope (internal/step) delivers resilience now.
- ⬜ **Integration tests** against live SSH/FTP/kubectl/Vault/cosign — logic unit-tested via injected transports; needs real infra/CI.

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
