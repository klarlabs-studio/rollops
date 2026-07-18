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

### [OPEN] — remaining (need operator specifics)
- **Marketing-sites digest→semver flip** — per-app `.rollops` imagePolicy edits in the app repos, gated on each having cut a semver release; operator's call per-app.
- **hermes/mnemos shared-service config ownership** pattern — a decision, not yet scoped.
- Follow-up: **manual `Verify` also skips the smoke test** (only metric analysis was added in #65) — small engine add if wanted.
