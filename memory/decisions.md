# Decisions — Rolloffs

Append-only. Superseded entries get `→ superseded [date]`, never deleted.

## 2026-06-08 — Scaffold & plan

- **Module path** = `github.com/klarlabs/rolloffs`. Repo dir is `rollops`; project name is `rolloffs` (matches all docs). Kept the mismatch; noted in README.
- **Packages by domain, not layer** (per klarlabs best-practice). `internal/` for engine guts, `pkg/` for the public Target plugin contract + conformance suite (community plugins import it).
- **Target contract is public** (`pkg/target`) so third-party gRPC plugins can implement it. Store stays `internal` (no external implementers expected in OSS core).
- **Domain entities live in `internal/rollout`** to avoid import cycles (Store imports rollout + pkg/target).
- **Roady plan = 1:1 task per requirement** (63 tasks). Inter-task deps NOT wired in Roady; build order documented in `roadmap.md` instead. Plan approved to unblock execution.
- **Build order: contracts → SQLite → engine → lifecycle/gate → targets → reconcile/rollback → interfaces → security → UI.** Dumb target (SSH/VM) before rich (K8s).
- **Resolved decisions from vision are constraints**, not choices to revisit: observability-free v1, Git-as-desired-truth, CEL-not-DSL, secrets-never-local, every-interface-authenticated, hard agent guardrails. Encoded in `.roady/spec.yaml` constraints and `AGENTS.md`.

## 2026-06-08 — Stack pinned + plan ordered

- **Module path = `go.klarlabs.de/rolloffs`** (changed from github.com/klarlabs/rolloffs) to match org vanity-import convention. Imports updated.
- **Stack components published + pinned** as direct deps (full build resolves):
  `go.klarlabs.de/statekit v1.8.0`, `axi v1.4.0`, `fortify v1.6.0`, `bolt v1.5.2`, `mcp v1.15.0`, `mnemos v0.19.0`; `github.com/felixgeelhaar/decisionkit v0.1.0`.
  Module names are `axi`/`mcp` (not `-go`). decisionkit root has no package — risk gate imports `decisionkit/risk`.
- **`internal/stack/stack.go`** blank-imports all 7 to keep them direct + prove the stack compiles together. Each import migrates into its consuming package as phases land.
- **Roady plan re-authored: flat 63 → 37 ordered tasks** (one per feature, big ones split) with `depends_on` chains across 8 phases. Smart-injection merges rather than replaces, so plan.json was stripped to the `t-*` tasks directly + re-approved. Dependency gating verified: only no-dep roots are `ready`.

## 2026-06-08 — Phase A foundation (config + store)

- **Config package** (`internal/config`): YAML surface, types, embedded JSON Schema (`SchemaJSON`), `Parse` (structural, version/kind gate, `KnownFields` rejects unknowns), `Validate`/`Load` (schema via santhosh-tekuri/jsonschema/v6 + semantic rules). Libs: `gopkg.in/yaml.v3`, `santhosh-tekuri/jsonschema/v6`.
- **CEL in `internal/condition`** (package `condition`, not `cel` — avoids clash with cel-go's `cel` pkg). Typed vars: criticality, environment, changeType, blastRadius, strategy, score. Strict compile (unknown var / syntax / non-bool rejected). `condition.Check` wired into config.Validate for `risk.sensitive` + `rollback.trigger`. Lib: `github.com/google/cel-go`.
- **Store interface CHANGED** (evolved from scaffold): added `SaveObservedState(ctx, TargetState)` + sentinel `store.ErrNotFound`. Reconciler must persist observed state; the scaffolded interface lacked a setter. t-store-iface stays "done" — this is a forward evolution, not a redo.
- **SQLite default backend** (`internal/store/sqlite`): pure-Go `modernc.org/sqlite` (no cgo → single binary). 4 tables (rollouts, target_state, schedules, history). `SaveRollout` upserts rollout + appends history in one tx (crash-safe per transition). WAL + busy_timeout for daemon/CLI coexistence. Times stored RFC3339Nano.
- **Time in tests**: normal Go `time.Now()`/`time.Date` fine here (the Date.now ban is workflow-script-only).
- **Feature branch per task** under `feat/...`; commits are atomic + conventional. Branches unmerged, awaiting review.
