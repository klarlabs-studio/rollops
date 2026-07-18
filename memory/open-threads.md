# Open threads — Rollops

## Blocking next work
- None blocking. v0.1.0 is cut, published to github.com/klarlabs-studio/rollops
  with a GitHub release, and importable as go.klarlabs.de/rollops. Next work is
  P3/studio scope or the notification channel decision.

## Resolved
- **Stack module paths** (2026-06-08): all 7 published + pinned in go.mod, full build resolves.
  Vanity imports: `go.klarlabs.de/{statekit v1.8.0, axi v1.4.0, fortify v1.6.0, bolt v1.5.2, mcp v1.15.0, mnemos v0.19.0}`.
  decisionkit on GitHub: `github.com/felixgeelhaar/decisionkit v0.1.0` (risk gate uses `/risk` subpkg).
  Note: module names are `axi` and `mcp` (not `axi-go`/`mcp-go`). Blank-import anchor at `internal/stack/stack.go`.

## TDD §17 open items (revisit as it grows)
- Config schema v1 concrete YAML shape + CEL hook points + version field — decided and implemented.
- gRPC plugin protocol contract + versioning — implemented; adapter hardened for nil RPC and invalid health states; process lifecycle/distribution remains future work before broad third-party support.
- Metric-based analysis interface (P2, Obvia seam) — experimental opt-in seam exists; keep disabled in v1 defaults.
- Multi-instance coordination (leader election) — studio/scale, deferred.
- UI act-vs-observe scope for v1 — closed. Read-and-act dashboard now has
  filter, attention queue, dense application list, derived health/sync/risk,
  detail summary, resource graph/list, diff, timeline, rollback, sync, approve,
  and reject. Next UI work should come from dogfood findings or P2/studio scope.
- Concrete RBAC role/permission taxonomy — first taxonomy exists; refine from dogfood evidence.

## Decisions needing operator input
- ~~Notification channel for approvals/failures (Telegram mentioned, P1).~~
  Resolved 2026-06-10: operator decided **email instead of Telegram**. Telegram
  notifier removed. Mail channels: briefkasten outbox (preferred — durable,
  retried; ROLLOPS_BRIEFKASTEN_URL/TO/TOKEN, MCP email.send) and direct SMTP
  (ROLLOPS_SMTP_ADDR/FROM/TO + optional USER/PASS). HMAC webhook unchanged.
  Also added docs/notifications.md, notify.FromEnv (shared CLI/daemon wiring),
  and a `rollops doctor` notify probe that sends a test event per channel.
- Default risk threshold + criticality weights — sensible defaults shipped, operator tunes via CEL.
- Release polish priority: `doctor` command, install/systemd packaging, first-run docs/examples, RBAC docs/defaults, plugin adapter hardening, dashboard workflow refinement, release-check aggregation, version metadata, changelog, and Roady drift cleanup are done.

## 2026-06-14 update

### Resolved
- **keel replacement** — DONE. 28 first-party deployments migrated to Rollops GitOps; keel ns deleted. Config-in-app-repo, image automation (semver+digest) live.
- **ArgoCD/Flux/Rollouts parity** — traffic routing, pluggable metrics, OCI+bucket sources, CRD health, multi-cluster all shipped (v0.11–v0.13).
- **Plugin marketplace** — search/info/install/list/update + 10 providers across 3 capabilities; flagconformance suite.
- **In-cluster rollopsd** — containerized, deployed, running the fleet on v0.16.0.

### [WAITING] operator
- Revoke leaked PAT `ghp_76ied…`; recreate `rollopsd-git`/`rollopsd-registry` privately (`read -rs`). Cluster works on the (leaked) token until then.

### [OPEN]
- **roady #34** — per-repo deploy keys / GitHub App for least-privilege multi-org git auth (replaces the broad classic PAT).
- Marketing sites (armada/brotwerk/kraftsport-coach/klarlabs) digest→semver: their CI now supports `site-v*`/`marketing-v*` tags; flip `.rollops` imagePolicy to minor after first semver release. Or stay continuous-deploy (digest).
- hermes/mnemos config lives in klarlabs-studio/mnemos repo (done); shared-service config ownership pattern may need revisiting.

## 2026-07-17 update

### Resolved
- **`manifestFrom` referenced manifest sources** (issue #57) — path/kustomize/helm shipped in **v0.26.0** (#58), non-breaking vs inline/flat keys, path-confined, drift keyed off rendered output, `plan`/`doctor` preview.
- **Rollback of referenced sources** (#59) — restores the captured rendered bytes (root-independent across CLI/UI/MCP/API/gRPC; fixes the latent daemon "referenced files changed since deploy" case).
- **ghcr image-push blockage (v0.19/v0.20 era) — GONE.** v0.26.0's goreleaser + Docker image jobs both passed; `rollopsd:v0.26.0` is on ghcr, and the cluster was rolled to it (Running/Ready).
- **Cluster upgraded to v0.26.0**; in-repo `deploy/kubernetes/rollopsd.yaml` re-synced from stale v0.24.0 (#62).
- **Leaked PAT `ghp_76ied…` REVOKED by operator (2026-07-17).** The lingering 2026-06-14 security thread is closed.
- **Flat-key deprecation decided:** keep the flat keys (`spec.helm`/`kustomize`/`manifest`/`oci`/`bucket`) working for back-compat, **not** slated for removal; new configs prefer `manifestFrom`. Documented in `docs/kubernetes-sources.md`.
- **Docs-PR merge gotcha fixed (#63):** dropped `paths-ignore` on the `pull_request` trigger so markdown/`memory/`-only PRs run CI and satisfy the required checks (no more BLOCKED/admin-only merges). `push`-to-main keeps its `paths-ignore`.

### Resolved (continued — features closed this session)
- **Metric analysis on manual Verify/Promote (#65):** analysis config persisted on the rollout (opaque JSON `rollout.Analysis`, migration 0008 threaded through the store); manual `Verify` + public `Promote` run the same metric gate as the auto path. `Promote` gated on the PUBLIC method (not `promoteWithNote`) so the auto path doesn't run analysis twice.
- **MCP per-caller transport auth (#66):** bearer-token→identity reusing `api.Authenticator`; fail-closed via mcp-go `WithAuthorize` (403) + `WithRequestContextFn`; `Tools` derive the caller per-request (defense-in-depth handler-level fail-close). **BREAKING on deploy — see WAITING.**
- **roady #34 git-auth — DESIGNED, ready to execute (#68).** Finding: GitHub App auth (auto-rotating installation tokens) *and* deploy keys already exist in the code (`git.Auth` + `internal/git/githubapp.go`) — #34 was never a build, only an operational migration. Landed `docs/git-auth-migration.md` + `deploy/kubernetes/rollopsd-git.example.yaml` + updated `rollopsd-watch.example.yaml`. Decisions: **separate App per org** (blast-radius isolation), cutover = `rollopsd-watch` ConfigMap edit + restart, uniform `contents: write` default. Execution is operator work → WAITING.

### [WAITING] operator
- **Before the next MCP-serving deploy: set `ROLLOPS_MCP_TOKENS`** (JSON `{"<token>":"<agent>"}`) and give every MCP caller an `Authorization: Bearer <token>`. #66 made the MCP surface **fail-closed** (no fallback agent) — it rejects all calls until tokens are configured. Merged to main but NOT deployed (cluster still on v0.26.0, which predates #66).
- **Execute the git-auth migration (roady #34)** per `docs/git-auth-migration.md`: create the two GitHub Apps (one per org) + keys, populate the `rollopsd-git` Secret, cut the `rollopsd-watch` ConfigMap over to App auth repo-by-repo, then delete the PAT. Optionally flag any watch-only repos to split read-only vs write scope (else uniform `contents: write`). Design + templates are merged; this is GitHub/cluster operator work.

- **Marketing-sites digest→semver — VERIFIED, no change (2026-07-18).** All three
  marketing-site configs (`armada-marketing`, `brotwerk-site`, `kraftsport-coach-site`)
  are correctly on `imagePolicy: mode: digest` tracking `:latest@sha`. Their ghcr
  images still publish only `latest` + `sha-<commit>` — **no `site-v*`/`marketing-v*`
  semver tags exist yet**, so digest is the only viable mode; flipping to semver now
  would break auto-deploy. The flip is gated on the operator cutting a semver
  marketing release first — a future operator decision, not a config edit. Staying
  on digest (continuous-deploy) is a valid end state.

- **hermes/mnemos config ownership — DECIDED (2026-07-18): per-product ISOLATED.**
  Org memory never crosses products; each product owns+pins its own mnemos (the
  fleet already conforms — pet-medical's own instance, senat's own PVC, devatlas
  optional-off, mnemos-repo config = hermes's instance). No config change needed;
  recorded in decisions.md. Not a shared pool.

### [OPEN] — remaining
- Future/optional: flip a marketing-site to semver *after* its CI publishes a
  `site-v*`/`marketing-v*` tagged image (see verified note above).
- **Operator-execution item** (not code): execute the git-auth migration per
  `docs/git-auth-migration.md` (create the two Apps + keys, cut the watch
  ConfigMap over, decommission the PAT).
- ~~Set MCP tokens before the next deploy~~ — **NOT a blocker for THIS cluster
  (verified 2026-07-18).** `ROLLOPS_MCP_ADDR` is **not set** on the rollopsd
  Deployment, and `main.go` only serves MCP when it is — so the daemon does not
  expose MCP at all and #66's fail-closed change cannot affect it. The standing
  note carried since 2026-07-17 was wrong for this deployment. It becomes a real
  prerequisite the moment MCP is enabled: set `ROLLOPS_MCP_TOKENS_FILE`
  (`docs/mcp-tokens.md`) in the same change that sets `ROLLOPS_MCP_ADDR`.
- **CHANGELOG is unwritten for #65, #66, #72, #74, #75 and #76.** The repo writes
  changelog entries at release time; batch all six at the next cut.
- **NEXT-DEPLOY IMPACT — AUDITED 2026-07-18, far smaller than feared.** All four
  behaviour changes were checked against the actual fleet:
  - **#66 MCP fail-closed — NO IMPACT.** MCP is not served (no `ROLLOPS_MCP_ADDR`).
  - **#75 env scrub — NO IMPACT.** Pulled all 71 `.rollops` configs from all 11
    watched repos: the only spec keys in use are `target`, `strategy`,
    `imagePolicy`, `verification`. **No `rollback:` block, no `smokeTest`, no
    `database:` hook anywhere in the fleet** — nothing runs config-sourced
    commands on the daemon host today, so there is no environment to scrub. Every
    `command:` found is inside a Kubernetes manifest (probes, initContainers);
    those run in the pod and are unaffected. Note pet-medical and senat-os run
    migrations as **initContainers**, not rollops DB hooks — also unaffected.
  - **#72 smoke-on-promote — NO IMPACT** (no smokeTest configured anywhere).
  - **#74 health-on-promote — the ONE real change.** A manual `promote` now runs
    a health check it previously skipped, so it can be refused on an unhealthy
    target; `promote --force` overrides. Rarely hit: the fleet promotes via the
    reconciler's auto path (`VerifyOrRollback`), not manual promote.
- **rollopsd is NOT GitOps-self-managed** (verified 2026-07-18): it appears in no
  watched repo's `.rollops`, so `deploy/kubernetes/rollopsd.yaml` is applied by
  hand. **Cutting a release therefore cannot auto-roll the daemon** — image
  automation only drives the watched apps.

## Resolved 2026-07-18

- **`verify` is now a real dry-run verb, and `promote` enforces what it dry-runs
  (#74, MERGED).** Closes the `Engine.Verify` dead-code question: it had no
  production callers (every surface called `Promote`; the reconciler uses
  `VerifyOrRollback`), so rather than delete it, it became the verb it was shaped
  like. `Verify` returns a `VerifyReport` (per-gate pass/fail/skipped/not-run +
  verdict) and changes nothing; a failing gate is a RESULT, errors are reserved
  for operational failures, which fail closed. **`gatesFromConfig` (auto) and
  `gatesFromRollout` (manual) now feed ONE `runGates`** — the structural fix for
  the drift #72 patched by hand; tests assert the dry run's verdict matches both
  `VerifyOrRollback`'s decision and whether `Promote` succeeds.
  `promote` gates on health+smoke+analysis with **`--force`** as the audited
  break-glass (mirrors `rollback --force`; bypass recorded on the rollout note
  AND in the audit trail). Promotion is now audited at all — `audit.ActionPromote`
  existed but was never emitted, so `Promote` takes the actor identity like
  Approve/Reject. Exposed on CLI (`rollops verify`, non-zero exit so
  `verify && promote` composes), HTTP, gRPC, MCP. **The web console never
  forces.** Docs: `docs/verify.md`.
- **Config-sourced commands no longer inherit the daemon env (#75, MERGED).**
  SECURITY. `confine.go` documents repo config as untrusted and able to name
  commands to run on the daemon host — but `execSmoke`/`execDBRollback` set no
  `cmd.Env`, so a smoke test from a watched repo could read `ROLLOPS_MCP_TOKENS`,
  `ROLLOPS_ADMIN_TOKEN`, `ROLLOPS_UI_PASSWORD`, `ROLLOPS_REGISTRY_TOKEN`, OIDC
  settings and cloud creds (`env | curl`). The **plugin host already did this
  right** (`buildPluginEnv`); the smoke/DB path never got the treatment. Both now
  share `security.ConfineEnv`. Default-**ON** (unlike the other opt-in
  confinement controls) — withholding secrets shouldn't need configuration.
  `ROLLOPS_ALLOWED_ENV` names extras, `*` restores inheritance. Tests spawn a
  real command and read its actual env; verified they fail without the fix.
  Docs: `docs/command-confinement.md` (also documents the three previously
  undocumented confinement controls).
- **MCP tokens load from a file, reloadable on SIGHUP (#76, MERGED).**
  `ROLLOPS_MCP_TOKENS_FILE` (mounted Secret) preferred over the env var, which
  still works; file wins when both are set. `api.SwappableTokenAuth` does an
  atomic swap so an in-flight request never sees a half-applied rotation.
  **Startup fails closed** (no known-good state to keep); **reload keeps current**
  on failure, so a typo can't lock every agent out mid-flight — hence the loader
  distinguishes "failed to load" from "loaded, and empty". Tokens stay
  credentials; roles stay in `ROLLOPS_POLICY_FILE`. Verified live against a
  running daemon: rotate file + SIGHUP swapped credentials with no restart (old
  token 403, new 200), and a malformed edit kept the working token serving.
  Docs: `docs/mcp-tokens.md`.
- **Manual `Verify`/`Promote` now run the smoke gate (#72, MERGED).** Closes the
  #65 follow-up. `spec.rollback.smokeTest` is captured on the rollout at deploy
  time as opaque JSON (migration **0009**, mirroring 0008/analysis); manual
  `Verify` and `Promote` run it in the auto path's order (health → smoke →
  analysis). No-op when unconfigured, so pre-0009 rows and health-only rollouts
  are unchanged; **fails CLOSED** on an undecodable descriptor. The auto path is
  untouched — it promotes via `promoteWithNote` (below the manual gate), so smoke
  still runs exactly once (regression test). Upgrade path verified against an
  existing db (`TestOpen_MigratesExistingDBForSmokeTest`).
  **BEHAVIOUR CHANGE on deploy:** manually promoting a rollout whose config has a
  `smokeTest` now actually executes that command on the daemon host (same
  confinement policy as the auto path); a non-zero exit blocks the promote.
  Not yet deployed — cluster is still on v0.26.0.
