# Status — Rolloffs

*Updated: 2026-06-08*

## Where we are

Greenfield scaffold complete. Module `go.klarlabs.de/rolloffs` (Go 1.26),
git on `main`, domain skeleton in place, builds clean (`go build ./...` OK).
All 7 stack deps pinned + resolving. Roady plan **ordered & approved: 37 tasks, 2 done, 35 pending**, dependency-gated.

**6/37 tasks done.** Phase A (contracts + config + store) complete. Engine (`t-engine-api`) unblocked.

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

All on branch `feat/config-schema` (4 commits) + scaffold on main (9848fa6). Branch unmerged, awaiting review.
Full suite green: config, condition, sqlite. `go vet ./...` clean.

## Next (ready tasks)

1. **t-engine-api** ← unblocked. Engine surface: plan/apply/verify/promote/rollback/observe/schedule, transport/storage-agnostic. Wires config+store+target. (`t-engine-plandiff`, `t-engine-locks` follow.)
2. **t-conformance** — target conformance suite (idempotency/fingerprint/health).
3. **t-config-perrepo** — per-repo config at branch+path (finishes config-model feature).

See `roadmap.md` for full phased order.

## Blocked / open

- None blocking. Stack confirmed + pinned (see decisions/open-threads).
- Repo dir is `rollops`, project/module is `rolloffs` — intentional, noted in README.
