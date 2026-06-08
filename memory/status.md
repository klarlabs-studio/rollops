# Status — Rolloffs

*Updated: 2026-06-08*

## Where we are

Greenfield scaffold complete. Module `go.klarlabs.de/rolloffs` (Go 1.26),
git on `main`, domain skeleton in place, builds clean (`go build ./...` OK).
All 7 stack deps pinned + resolving. Roady plan **ordered & approved: 37 tasks, 2 done, 35 pending**, dependency-gated.

## Done this session

- git init + go.mod + domain directory skeleton (cmd/, internal/, pkg/).
- Two core contracts written and building:
  - `pkg/target.Target` (Apply/Observe/Health + Manifest/Result/Fingerprint/HealthStatus).
  - `store.Store` + `rollout` entity model (Rollout/TargetState/ScheduledRollout/RolloutRecord/Dependency).
- README, AGENTS.md, .gitignore.
- Roady: full spec (22 features, 9 constraints) from vision/TDD, plan generated + approved.
- Agent OS memory scaffolded.

## Next (top 3)

1. **config-model** — config schema v1 + version field, loud Go validation, CEL hook points. Foundation; unblocks risk gate + lifecycle.
2. **store-backend** — SQLite backend implementing `store.Store` + migrations + crash-safe per-transition persist.
3. **engine-library** — engine API surface (plan/apply/verify/promote/rollback/observe/schedule) + plan/diff.

See `roadmap.md` for the full phased build order.

## Blocked / open

- None blocking. Stack confirmed + pinned (see decisions/open-threads).
- Repo dir is `rollops`, project/module is `rolloffs` — intentional, noted in README.
