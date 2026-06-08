# Status — Rolloffs

*Updated: 2026-06-08*

## Where we are

Greenfield scaffold complete. Module `go.klarlabs.de/rolloffs` (Go 1.26),
git on `main`, domain skeleton in place, builds clean (`go build ./...` OK).
All 7 stack deps pinned + resolving. Roady plan **ordered & approved: 37 tasks, 2 done, 35 pending**, dependency-gated.

## Done this session

- git init + go.mod (`go.klarlabs.de/rolloffs`) + domain skeleton (cmd/, internal/, pkg/).
- Two core contracts building: `pkg/target.Target`, `store.Store` + `rollout` model.
- All 7 Klarlatz stack deps pinned + resolving (`internal/stack` anchor).
- Roady: spec (23 features, 9 constraints) + **37 dependency-ordered tasks**, approved.
- README, AGENTS.md, vision/TDD in-repo, Agent OS memory.
- **t-config-schema DONE** (branch `feat/config-schema`, commit 1d55d34): `internal/config`
  RolloutConfig v1 types + embedded JSON schema (`config.SchemaJSON`) + version-gated `Parse`
  (KnownFields rejects unknown keys) + example + 6 passing tests.

Commits: main has scaffold (9848fa6); `feat/config-schema` has config (1d55d34) — unmerged, awaiting review.

## Next (ready tasks)

1. **t-config-validate** — deep validation (required fields, enum membership, oneOf health, CEL well-formedness) using embedded `SchemaJSON`. Loud rejection.
2. **t-config-cel** — CEL eval for risk.sensitive / rollback.trigger / promotion criteria.
3. **t-store-sqlite** — SQLite backend for `store.Store` + migrations + crash-safe persist.
4. **t-conformance** — target conformance suite (idempotency/fingerprint/health).

`t-config-perrepo` also ready. See `roadmap.md` for full phased order.

## Blocked / open

- None blocking. Stack confirmed + pinned (see decisions/open-threads).
- Repo dir is `rollops`, project/module is `rolloffs` — intentional, noted in README.
