# Changelog

## v0.28.0 - Deploy through a protected branch

Image automation could not deploy to a repository whose branch is protected.
rollopsd writes an image bump by committing it to the tracked branch and
pushing — and a branch that requires pull requests or status checks rejects that
push (`GH006: Changes must be made through a pull request`). The failure was
quiet: the config showed `=error` in the reconcile summary and the target simply
stopped updating, with nothing but a daemon log line to say why.

**New: `imagePolicy.writeback: pull-request`.** The default stays `push`, so
existing configs are unchanged. In the new mode rollopsd never writes the
tracked branch. It commits the bump on a deterministic branch
(`rollops/image/<config-name>`), pushes it, and opens — or refreshes — a pull
request into the tracked branch, enabling GitHub auto-merge so the change lands
the moment its required checks pass.

The load-bearing property is that PR mode **does not deploy in the same cycle**.
The bump lives only on the PR, so the cluster never leads Git; the deploy happens
through the ordinary reconcile once the PR merges and the tracked branch advances
to carry it. A protected branch and the running target stay consistent.

```yaml
imagePolicy:
  mode: digest
  allowMutableTags: true
  writeback: pull-request   # default: push
```

- **Idempotent.** A duplicate-head response is resolved to the existing PR and
  reused, so polling every interval neither errors nor spawns pull requests.
- **Auto-merge is best-effort.** A repository with auto-merge disabled (or a
  token lacking permission) still gets the PR opened and waits for a human —
  writeback is unblocked either way, never stuck in an error.
- GitHub only for now.

### Upgrading

- No action for `push`-mode configs; behaviour is unchanged.
- For a config on a protected branch, set `imagePolicy.writeback: pull-request`.
  The daemon's git token needs **`pull-requests: write`** in addition to the
  existing contents read+write (a GitHub App installation or a PAT scoped to the
  config repo). Only upgrade the configs after the daemon is on v0.28.0 — an
  older daemon rejects the unknown `writeback` field.

## v0.27.0 - One post-deploy gate, and secrets that stay in the daemon

This release closes a class of bug rather than a single one: the automatic and
manual paths through the post-deploy gate had drifted apart, gate by gate, and
now share a single implementation. It also fixes a secret-exposure bug found
while auditing where MCP tokens live. **Four behaviour changes — see Upgrading.**

- **Config-sourced commands no longer inherit the daemon environment.** Rollops
  runs commands the repo config names (smoke tests, database migrate/rollback
  hooks), and the confinement policy treats that config as untrusted. Those
  commands were spawned with no `cmd.Env`, so they inherited the daemon's whole
  environment — `ROLLOPS_MCP_TOKENS`, `ROLLOPS_ADMIN_TOKEN`, `ROLLOPS_UI_PASSWORD`,
  `ROLLOPS_REGISTRY_TOKEN`, the OIDC settings, and any platform-injected cloud
  credentials. A poisoned or simply careless repo could read every daemon secret.
  The plugin host already confined its subprocess environment; both paths now
  share one implementation. A confined command gets `PATH`/`HOME`/`TMPDIR` plus
  whatever `ROLLOPS_ALLOWED_ENV` names, and `*` restores full inheritance. Unlike
  the other confinement controls this one is **default-on** — withholding secrets
  should not require configuration. See `docs/command-confinement.md`, which also
  documents the three previously undocumented confinement controls. (#75)

- **`rollops verify`: dry-run the post-deploy gate.** A new verb that answers
  "would this promote?" It runs the same health, smoke and metric-analysis gates
  as the automatic path, reports each one (`pass` / `fail` / `skipped` /
  `not-run`), and **changes nothing** — no phase transition, no promotion, no
  rollback, no history entry. A failing gate is a result, not an error; errors
  are reserved for operational failures, which fail closed. The CLI exits
  non-zero on a failed gate, so `rollops verify <id> && rollops promote <id>`
  composes. Available on the CLI, `POST /v1/verify`, `RolloutService.Verify`, and
  the `rollouts.verify` MCP tool. Authorized as **promote** permission, not a read
  permission: the gates really run, and a smoke test executes a command on the
  daemon host. See `docs/verify.md`. (#74)

- **`promote` enforces exactly what `verify` dry-runs.** Manual promotion had
  accumulated gates piecemeal — metric analysis in #65, the smoke test in #72,
  health never — which left `verify` able to report a failure `promote` would
  shrug off. Promotion now gates on health, smoke and analysis, in that order,
  from descriptors captured on the rollout at deploy time. `promote --force`
  (`-f`, and `force: true` on the HTTP/gRPC/MCP surfaces) is the break-glass
  override for when a gate is itself wrong — a flaky probe, a metrics backend
  that is down. The bypass is never silent: it is recorded on the rollout's note
  and in the audit trail, attributed to the caller. The web console never forces.
  (#65, #72, #74)

- **Promotions are audited.** `audit.ActionPromote` was defined but never
  emitted, so nothing recorded who completed a rollout. `Promote` now takes the
  actor identity, as `Approve`/`Reject` already did, and every promotion —
  forced or not — lands in the audit trail. (#74)

- **One gate runner behind both paths.** The automatic path (`VerifyOrRollback`)
  and the manual paths now resolve the same gate set and execute it through one
  runner, with one ordering and one short-circuit rule. Tests assert that a dry
  run's verdict matches both what `VerifyOrRollback` decides and whether
  `Promote` succeeds — so the two can no longer disagree silently, which is how
  the missing gates went unnoticed in the first place. (#74)

- **MCP bearer tokens can be loaded from a file, and rotated without a restart.**
  `ROLLOPS_MCP_TOKENS_FILE` points at the same JSON as `ROLLOPS_MCP_TOKENS`,
  typically a mounted Secret. Token material no longer has to sit in the daemon
  environment, where it is inherited by subprocesses and visible in
  `/proc/<pid>/environ`, `docker inspect` and crash dumps. `SIGHUP` now reloads
  the tokens alongside the RBAC policy, through an atomic swap, so an in-flight
  request never sees a half-applied rotation. Startup fails closed when the token
  source is unreadable (there is no known-good state to keep); a failed **reload**
  keeps the current tokens, so a typo cannot lock every agent out mid-flight. The
  env var still works, and the file wins when both are set. See
  `docs/mcp-tokens.md`. (#76)

- **Per-caller MCP authentication.** Each MCP caller presents a bearer token
  resolving to a distinct agent identity, so RBAC authorizes each caller as
  itself instead of treating every connection as one fixed agent. The surface is
  **fail-closed**: with no tokens configured, or a token that does not resolve,
  the request is rejected before any tool runs — there is no fallback identity.
  (#66)

- **Docs and CI.** A git-auth migration runbook for moving off the classic PAT to
  per-org GitHub Apps, with copy-paste templates (#68). Markdown- and
  `memory/`-only pull requests now run CI, so they can satisfy the required
  checks instead of needing an admin merge (#63).

### Upgrading

1. **Configure MCP tokens before deploying** if you serve MCP. The surface is
   fail-closed, so a deploy without tokens rejects every agent call. Prefer
   `ROLLOPS_MCP_TOKENS_FILE`.
2. **Check your smoke tests and database hooks for environment reads.** They no
   longer inherit the daemon's environment. Name what they need in
   `ROLLOPS_ALLOWED_ENV`, or set `ROLLOPS_ALLOWED_ENV=*` to keep the old
   behaviour while you migrate.
3. **A manual promote can now be refused.** If a rollout's config declares a
   smoke test or metric analysis, or its target is unhealthy, `promote` runs
   those gates and stops on failure. Use `rollops verify <id>` to see which gate
   would fail, and `promote --force` to override.
4. **Rollouts deployed before this release** report their smoke and analysis
   gates as `skipped`. Both descriptors are captured on the rollout at deploy
   time (migrations `0008` and `0009`, applied idempotently on an existing
   database), so rollouts that predate them carry none. The health gate always
   runs.

## v0.26.0 - Referenced manifest sources (Kustomize / Helm / file)

- **`manifestFrom`: reference manifests instead of inlining them.** The Kubernetes
  target can now resolve its desired manifest from a referenced source rendered at
  plan/apply time — `manifestFrom: { path | kustomize | helm }` — instead of
  requiring the full Deployment inlined under `spec.target.spec.manifest`. Teams
  that manage manifests with Kustomize overlays or Helm no longer keep a second,
  drift-prone inline copy. Relative paths resolve against the config file's own
  directory; Kustomize/Helm are rendered by shelling out to `kubectl kustomize` /
  `helm template` (no Kubernetes SDK pulled into the core). `manifestFrom` is
  exclusive, but the existing inline `manifest` and legacy flat keys keep working
  unchanged. `rollops plan` now prints the rendered manifest and `rollops doctor`
  probes for `kubectl`/`helm`. Referenced sources key drift off the **rendered
  output**, so an edit to a referenced Kustomize/Helm/path file is detected even
  under shallow verification. (#57, #58)
- **Rollback restores exactly what was deployed.** For referenced sources the
  rendered manifest bytes are captured at apply time and persisted with the
  rollout, so a rollback re-applies the exact deployed manifest instead of
  re-rendering the source — which could differ if the referenced files changed
  since, or be unavailable where no checkout is at hand (the manual CLI / web UI /
  MCP / HTTP API / gRPC rollback path). (#59)
- **Path confinement + safe rendering.** Referenced paths are confined to the
  config-file root (absolute paths and `..` escapes rejected); Kustomize/Helm run
  at safe defaults (no exec plugins, no post-renderer). Remote Kustomize/Helm URLs
  pass through unchanged.

## v0.25.0 - Image tag pagination

- **Image automation follows tags/list pagination.** The registry tag scan now
  follows pagination on the OCI `tags/list` endpoint, so the newest tags on
  registries that paginate are no longer missed and a freshly published tag is
  reliably detected. (#55)

## v0.24.0 - Image-automation coverage log

- **Per-tick image-automation coverage summary.** Each reconcile now logs one line
  per repo naming EVERY config and its image-automation decision, e.g.
  `image automation senat: 3 config(s) [senat-api=current senat-runtime=bumped senat-web=current]`.
  Previously only bumps and errors logged, so a config that was never considered
  (or silently up-to-date) was indistinguishable from one that was — a skip was
  invisible. Bumps and errors still get their own detailed line.

## v0.23.0 - Decouple image automation from reconcile

- **Reconcile: image automation no longer starved by a blocked rollout.** In a
  repo of many configs, `tickOne` interleaved the two phases per config — image
  automation (registry scan + git bump) then reconcile (a health-gated progressive
  rollout that BLOCKS while it advances). So a slow or stuck rollout for one config
  starved the image bumps of every config after it in the loop, and a freshly
  published tag could sit undeployed indefinitely. Split into two phases: image
  automation for EVERY config first, then reconcile each — detecting a new tag and
  committing it to Git no longer depends on any other target's rollout finishing.
  Regression-tested (`TestWatcher_ImageAutoDecoupledFromReconcile`).

- **Deploy: fix the mTLS CA chain.** The v0.21.0 Kubernetes manifest issued the
  server cert directly from a self-signed `ClusterIssuer`, so the mounted
  `ca.crt` was the server's own leaf and no client cert could ever verify —
  mutual TLS was effectively non-functional. Reworked to a proper chain
  (self-signed issuer → root CA `Certificate` → CA `Issuer` → server + client
  certs), so client certs issued from the same CA `Issuer` verify against
  `ROLLOPS_TLS_CLIENT_CA`. Added `deploy/kubernetes/rollops-client-cert.example.yaml`
  for issuing per-caller client certs, and corrected `docs/tls.md`. Deploy-only;
  no daemon code or image change.
- **Deploy: make the manifest safe to re-apply.** The `rollopsd-watch` ConfigMap
  (a repo's live reconcile list) is no longer defined in `rollopsd.yaml` — a
  wholesale `kubectl apply` would have overwritten a running fleet's watch list
  with the 2-repo example. Moved it to `deploy/kubernetes/rollopsd-watch.example.yaml`
  (create once, manage separately). Flagged the `rollopsd-data` PVC and its
  storageClass as environment-specific, since the Deployment's `claimName` must
  match the PVC that actually holds the SQLite DB.

## v0.21.0 - Native TLS + mTLS (zero-trust transport)

rollopsd now terminates TLS itself on every network listener instead of relying
on a plaintext bind behind a proxy. The posture is zero-trust by default.

- **TLS 1.3 on every non-loopback listener** (HTTP, gRPC, MCP). Certs come from
  `ROLLOPS_TLS_CERT` / `ROLLOPS_TLS_KEY` (server keypair, PEM).
- **mTLS on the machine control plane.** With `ROLLOPS_TLS_CLIENT_CA` set, the
  programmatic REST API, gRPC, and MCP require a verified client certificate.
  The REST API and web console share one HTTPS listener
  (`VerifyClientCertIfGiven`); the API handler rejects requests without a
  verified client cert (`401`), giving per-surface mTLS on a single port.
- **The web console (UI) stays server-TLS + OIDC/session auth** — it does not
  require a client cert, because browsers can't present one.
- **Certificate hot-reload.** The server cert is served through a
  `GetCertificate` callback that re-reads the keypair when the file's mtime
  changes, so cert-manager (or any) rotation is picked up without a restart; the
  last-good keypair is kept in service across a transient bad rotation.
- **Deploy:** the bundled Kubernetes manifest now issues the server cert via a
  cert-manager `Certificate` (self-signed `ClusterIssuer` default — swap for your
  real CA), mounts `rollopsd-tls`, binds `:8443`, and probes over HTTPS. See the
  new `docs/tls.md` for env vars, per-surface behavior, client-cert issuance, and
  the loopback+mesh alternative.
- **BREAKING: `ROLLOPS_ALLOW_PLAINTEXT` is removed.** A non-loopback bind now
  requires `ROLLOPS_TLS_CERT` / `ROLLOPS_TLS_KEY` — there is no override. **Action
  required:** either provide a server keypair (and optionally a client CA for
  mTLS), or bind loopback (`ROLLOPS_ADDR=127.0.0.1:...`) behind a reverse proxy /
  sidecar mesh that provides encryption at the network boundary. A deployment
  that previously set `ROLLOPS_ALLOW_PLAINTEXT=1` on a routable bind will now
  refuse to start until TLS is configured.

## v0.20.0 - Security Hardening: Rollback Safety, Console RBAC, Supply Chain, Confinement

A deep security-and-correctness review of the whole daemon — reconcile/apply,
progressive delivery, the plugin host, the git/image supply chain, secrets, and
the control-plane APIs — surfaced and closed 3 critical, 12 high, and a batch of
medium/low issues. All fixes ship with tests.

### Rollback & progressive-delivery safety
- **Crashloop-on-arrival now auto-rolls-back.** A health-gate failure *during* a
  deploy previously marked the rollout `rolled-back` but left the broken version
  live; it now reverts to the last-good manifest when `rollback.auto` is set.
- **Rollback restores traffic and disables the flag.** Canary weight is reset to
  stable and the coupled feature flag disabled on every rollback path — a failed
  canary no longer keeps serving production traffic. The delivery descriptor is
  persisted, so manual and agent rollbacks reset it too.
- **Reconcile rolls back to the prior manifest**, not the just-applied one.
- **Fail-closed gates.** A drift/deep-diff error under `verification: full` and a
  post-deploy target-build error now count as failures instead of silently
  passing; metric analysis rejects an impossible `failureLimit >= count` config
  that could promote a canary which failed every check.
- **Emergency freeze is honored** by scheduled applies and by approvals; approval
  enforces four-eyes (approver != initiator; opt out with
  `ROLLOPS_ALLOW_SELF_APPROVE=1`).

### Access control & transport
- **The web console now enforces RBAC.** Console actions run as the authenticated
  principal (OIDC identity + groups, or the configured UI user) and are checked
  against the same policy as the API — previously every console action ran as a
  hardcoded `admin`. Audit is now correctly attributed.
- **JWKS refresh hardened** against an unauthenticated DoS (client timeout,
  off-lock fetch, single-flight, unknown-kid negative cache). Constant-time
  bearer-token comparison; explicit HTTP server timeouts.
- **Plaintext bind is fail-closed.** The daemon refuses a non-loopback HTTP/gRPC
  bind without TLS unless `ROLLOPS_ALLOW_PLAINTEXT=1`. **Action required:** a
  deployment that binds `:8080` behind a TLS-terminating proxy/ingress must set
  `ROLLOPS_ALLOW_PLAINTEXT=1` — the bundled Kubernetes manifest now does.

### Supply chain
- **Digest pins are preserved** across semver image updates — no silent
  immutable->mutable downgrade; semver updates re-pin to the resolved digest.
  Optional `spec.imagePolicy.allowedRegistries`.
- **Registry credentials are bound to the registry host** and no longer sent to
  an attacker-controlled Bearer `realm` (or over cleartext).
- **Poisoned-repo hardening:** config reads reject symlinks and are size-capped
  (no OOM); optional `ROLLOPS_REQUIRE_SIGNED_COMMITS` commit-signature gate;
  anchored tag-filter patterns; overflow-safe semver parsing.

### Plugin host
- **Plugins no longer inherit the daemon's environment.** An explicit allowlist
  (`ROLLOPS_PLUGIN_ALLOWED_ENV`, deny-by-default) replaces full `os.Environ`
  inheritance, so a plugin can't read the daemon's secrets. The manifest RPC is
  deadline-bounded (a stalled plugin can't hang the daemon). Optional
  `--require-signature` / `ROLLOPS_PLUGIN_REQUIRE_SIGNATURE`; unknown plugin risk
  classes fail closed; orphaned plugin process groups are reaped on launch error.

### Multi-tenant confinement (opt-in, default off)
- `ROLLOPS_ALLOWED_COMMANDS` — allowlist config-sourced smoke/DB commands,
  preventing arbitrary command execution from repo config.
- `ROLLOPS_ALLOWED_NAMESPACES` — restrict applies to named Kubernetes namespaces.
- `ROLLOPS_CONFINE_TARGET_CLUSTER` — ignore repo-supplied `kubeconfig`/`context`
  so a tenant repo cannot target another cluster.

## v0.18.0 - Interface Completeness: Every Op on Every Surface

Every rollout operation is now reachable from every interface (CLI, gRPC, REST,
MCP, UI), surfaced during an end-to-end wiring audit. All additive.

- **Rollout note in Status.** The latest transition note (e.g. `database
  rollback: succeeded`, a post-promote migration failure) is persisted on the
  rollout row and returned by Status over gRPC, REST, and MCP — previously only
  CLI/UI history showed it.
- **Promote / Approve / Reject over gRPC, REST, and MCP.** These were UI-only
  (approve/reject) or CLI-in-process (promote); agents and CI can now drive them.
  Approve/reject use `rollouts.approve`; promote uses the new `rollouts.promote`
  permission.
- **CLI `approve` / `reject`** subcommands; **UI Promote button** (shown while a
  rollout is verifying) and the rollback modal's **Force** checkbox.
- **Emergency freeze on every interface.** The kill-switch (`security.Freeze`)
  is now runtime-togglable everywhere — `rollops freeze [reason]` / `unfreeze`,
  the `Freeze` gRPC RPC, `POST /v1/freeze`, the `rollouts.freeze` MCP tool, and a
  UI freeze toggle + banner — gated by `rollouts.freeze`. While frozen, applies
  are blocked but promote/rollback stay available so recovery is never blocked.
  State is in-memory and does not survive a daemon restart.

## v0.17.0 - Enterprise Hardening: DB Lifecycle, RBAC, SSO, Supply Chain, Gateway Routing

Hardening surfaced by dogfooding the keel→Rollops fleet cutover and by closing
the remaining gaps against ArgoCD/Flux/Argo Rollouts. All changes are additive
and backward-compatible.

- **Database lifecycle.** A `spec.database` block makes schema changes a
  first-class part of the rollout (Flagger does this via DIY webhooks; Rollops
  makes it config):
  - `migrate` runs a forward migration at deploy time (`when: pre-deploy`,
    default) or after promotion (`when: post-promote` — contract / backfill); a
    failed pre-deploy migration aborts the deploy.
  - `rollback` (supersedes the deprecated `spec.rollback.database`, still
    honoured) now runs on **every** rollback path — manual and agent, not only
    auto — via a command persisted on the rollout.
  - `backwardCompatible` gates rollback: a release that ran a
    non-backward-compatible migration with no reverse command is **blocked**
    unless forced (`rollback --force`, `force: true`); auto-rollback bypasses.
  - `rollops plan` previews the pending migration.
- **Image automation.** Digest-pinned `image` refs under a semver policy are
  migrated once to the matching semver tag (reverse digest→tag lookup), so semver
  automation can take over.
- **Per-repo least-privilege git auth.** GitHub App installation tokens —
  short-lived, auto-rotating, per-installation (multi-org) — minted from an app
  key (`githubAppId`/`githubInstallationId`/`githubAppPrivateKeyFile` in
  `watch.json`). Replaces sharing a broad PAT across repos.
- **RBAC, configurable.** `ROLLOPS_POLICY_FILE` defines custom roles + bindings
  (incl. `group:` from OIDC) without recompiling; env-scoped grants now work via
  `spec.target.env`; **SIGHUP hot-reloads** the policy (atomic swap, bad file
  kept-current).
- **SSO depth.** OIDC now verifies RS256/384/512 and ES256/384/512 against the
  IdP's **JWKS** (cached, key-rotation aware) with optional OIDC discovery —
  no shared secret or external proxy needed. HS256 still supported.
- **Plugin supply chain.** Optional **cosign** signature verification
  (`ROLLOPS_PLUGIN_PUBLIC_KEY`, stdlib ECDSA/Ed25519/RSA) on top of the sha256
  pin; `needs_confirmation` is now a load-time gate (`ROLLOPS_PLUGIN_CONFIRM`);
  per-tool risk class feeds effective-risk policy admission.
- **Built-in Gateway API traffic router.** `trafficRouting.provider: gateway`
  shifts canary weight by patching an `HTTPRoute`'s `backendRefs` — progressive
  delivery with no plugin to install. Plugin routers remain for other meshes.

Note: a plugin declaring `needs_confirmation` will not load unless named in
`ROLLOPS_PLUGIN_CONFIRM` (or `*`). No shipped plugin declares it.

See also `docs/design/multi-cluster-scale.md` — a draft RFC scoping
ApplicationSet-style multi-cluster fan-out (not yet implemented).

## v0.16.0 - Fleet GitOps: Many-App Reconcile, Private Repos, Image Automation

Everything needed to run a whole cluster's rollouts from Git — a keel
replacement, validated by migrating a live production fleet (28 deployments) off
keel onto Rollops.

- **Many configs per repo.** `config.LoadAllFromDir` — a watched repo path can be
  a directory; the reconciler loads and reconciles every `*.yaml` independently,
  so one repo (or many) manages a whole cluster.
- **Private config repos.** `ROLLOPS_WATCH` entries accept `token`/`tokenFile`/
  `deployKeyPath`; the git layer now actually applies an https token (previously
  declared but unused) as an `Authorization` header, never written to disk.
- **Registry-poll image automation** (`imagePolicy`). The daemon scans the
  registry (Docker Registry v2 + bearer challenge; ghcr/Docker Hub/…) for newer
  tags of `spec.target.spec.image` and writes the bump back to Git (commit +
  push) — the keel-style "new tag → deploy", GitOps-native:
  - semver modes `major`/`minor`/`patch`/`any` (+ optional tag pattern); commit-
    SHA and `latest` tags are ignored safely, never selected.
  - `digest` mode pins a mutable tag's (`:latest`) manifest digest to
    `repo:tag@sha256:…` and redeploys when the digest moves (keel "force" parity).
  - a new `image` field on the Kubernetes target overrides the rendered
    manifest's container image so the bump reaches the workload. Enable on the
    daemon with `ROLLOPS_IMAGE_AUTOMATION=1` (+ registry creds for private). See
    `docs/image-automation.md`.
- **Containerized daemon + Kubernetes deploy.** A `Dockerfile` (pure-Go, embedded
  UI, kubectl+git on Alpine), `deploy/kubernetes/rollopsd.yaml` (namespace, SA,
  RBAC, watch ConfigMap, PVC, Deployment, Service), `cutover-patch.yaml`, and
  `docs/deploy-kubernetes.md` + `docs/keel-migration.md`.
- **Reliability/security fixes** surfaced dogfooding the live cutover:
  - image automation is best-effort — a scan/push failure no longer aborts the
    reconcile of that app.
  - the watcher logs per-tick outcomes (was silent); one unreachable repo is
    logged and skipped instead of crashing the whole daemon.
  - git tokens are redacted from error messages (an `http.extraheader` had leaked
    into a logged command on a clone failure).

## v0.15.0 - Dogfood Fixes (live k3s)

- **Full drift verification** (`verification: full`). The default shallow
  stamped-checksum marker misses out-of-band field edits that leave the marker
  intact (e.g. `kubectl set image`). Full mode additionally diffs live state
  against the desired manifest in `plan`, reporting any divergence as drift
  (`live drifted from desired …`). Found while dogfooding on a live k3s cluster.
- **Apply corrects drift even when the stamp matches.** `Target.Apply`
  short-circuited on a matching stamped checksum, so an out-of-band field edit
  (which preserves the annotation) was never reconciled. Apply now confirms with
  a live diff before skipping: matching stamp + empty diff = no-op; matching
  stamp + non-empty diff = re-apply. The GitOps self-heal path.
- `plugin install` pin hint no longer hardcodes `featureFlags:` — it now points
  at the matching spec block for the plugin's capability (featureFlags /
  trafficRouting / analysis).

## v0.14.0 - Bucket Sources + CloudWatch

- **Object-storage bucket source for the Kubernetes target** (`bucket` spec
  block) — the Flux `Bucket` source. Sync desired state from `s3://`
  (`aws s3 sync`) or `gs://` (`gsutil rsync`) to a temp dir, then render via
  `kubectl kustomize` (default) or a single `file`. Shares the render path with
  the `oci` source. See `docs/kubernetes-sources.md`.
- **CloudWatch metric provider** — `rollops-plugin-cloudwatch`, the second
  `metricprovider` plugin. Resolves a JSON CloudWatch metric query to a scalar
  via the `aws` CLI (`get-metric-statistics`); gates a canary on CloudWatch
  metrics. The marketplace now spans ten plugins across three capabilities.

## v0.13.0 - OCI Sources, CRD Health, Multi-Cluster

- **OCI artifact source for the Kubernetes target** (`oci` spec block) — the Flux
  `OCIRepository` model. Pull a non-Helm OCI artifact (a manifest bundle or
  kustomize tree) with `oras pull`, then render it via `kubectl kustomize`
  (default) or apply a single `file` verbatim. Desired state can now live in an
  OCI registry, not only a Git checkout.
  - OCI **Helm charts** (`helm.chart: oci://…`) and HTTP Helm repos were already
    supported by the Helm renderer; documented in `docs/kubernetes-sources.md`
    alongside the new artifact source.
- **CRD health assessment.** Health is no longer limited to rollout-able
  workloads. Any other kind (a CRD — cert-manager `Certificate`, Argo `Rollout`,
  Crossplane, …) is assessed from its `status.conditions` like Argo CD's health
  checks: healthy when `Ready`/`Available`/`Succeeded` is `True`, unhealthy (with
  reason + message) when `False`, progressing otherwise. `healthCondition` pins a
  custom condition type. Standard workloads still use `kubectl rollout status`.
- **Multi-cluster targeting.** A Kubernetes target can name its own `kubeconfig`
  file (alongside the existing `context`), so one daemon drives rollouts across
  many clusters — each with its own credentials — without a central cluster
  registry. Per-target reconcile / drift / health / rollback run independently;
  cross-cluster ordering uses `dependsOn`. See `docs/multi-cluster.md`.

## v0.12.0 - Pluggable Metric Providers

- **Metric-provider plugin capability** (`metricprovider`). Rollout analysis is
  no longer Prometheus-only: any metrics backend (Datadog, CloudWatch, a custom
  service) can drive the analysis gate as a sha256-pinned plugin declaring the
  `metricprovider` capability with a `query_metric` tool.
  - `pkg/plugin.ServeMetricProvider` + `MetricProvider` interface (`Query`).
  - `analysis.plugin` + `analysis.sha256` spec fields (schema + validation):
    when set, the engine launches the plugin per analysis run instead of the
    built-in Prometheus provider. `internal/metricplugin` host adapter;
    `WithMetricsProviderBuilder` seam.
  - Built-in Prometheus is unchanged. See `docs/metric-analysis.md`.

## v0.11.0 - Real Canary Traffic Routing

- **Traffic-router plugin capability** (`trafficrouter`). A weighted canary now
  shifts real network traffic, not just bakes: as the canary advances through
  its weight steps, Rollops drives a traffic-router plugin's `set_weight` tool
  to send that percentage of live traffic to the canary backend (the rest to
  stable) — the Argo Rollouts model, via the same sha256-pinned, safety-validated
  plugin mechanism as targets and feature flags.
  - `pkg/plugin.ServeTrafficRouter` + `TrafficRouter` interface (`SetWeight`).
  - New `trafficRouting` spec block (plugin/sha256/route/namespace/stableService/
    canaryService), schema + validation. Engine drives the router per canary step
    alongside feature flags; best-effort (audited, never aborts the deploy).
  - Composable with `featureFlags`: traffic routing shifts network traffic;
    feature flags shift application exposure. See `docs/traffic-routing.md`.

### Commercial Flag Providers

- Two more feature-flag provider plugins, covering the major commercial
  platforms, each in its own public repo and sha256-pinned in the registry:
  **launchdarkly** (environment fallthrough rollout, per-mille weights) and
  **split** (default-rule treatment buckets). Both pass `pkg/flagconformance`.
  The marketplace now spans seven providers — commercial (LaunchDarkly, Split),
  OSS (Unleash, GrowthBook, PostHog), Flagsmith, and the OpenFeature/flagd
  standard.

## v0.10.0 - Flag-Provider Conformance Suite

- `pkg/flagconformance` — a shared contract test every feature-flag provider
  plugin should pass, the analogue of `pkg/conformance` for targets. `Run`
  drives a provider through the canary contract: accepts the full 0–100 range
  (incl. boundaries), is idempotent across repeated applies, drives the disabled
  state, and honors context cancellation. Composable `Check*` functions too.

### More Marketplace Plugins

- Four new feature-flag provider plugins published to the marketplace registry,
  each in its own public repo, each sha256-pinned: **unleash** (flexibleRollout
  percentage), **posthog** (flag rollout percentage), **growthbook** (rollout
  rule coverage), and **openfeature** (flagd fractional targeting). Install any
  with `rollops plugin install <name>`.

### Plugin Update

- `rollops plugin update` — compares installed plugins against the marketplace
  registry and reports which are `up to date`, `outdated (old -> new)`, or
  `unknown (not from this registry)`. Each installed binary is identified by
  matching its sha256 to a published artifact, so a plugin renamed at install
  time is still recognised. Dry-run by default; `--apply` upgrades outdated
  plugins in place, running the same sha256-pin and cosign checks as a fresh
  install and printing the new pin. An optional name limits it to one plugin.
- Internal: install and update share one `resolveAndInstall` path, so both
  enforce identical pin + signature verification.

## v0.9.0 - Plugin Lifecycle + Automatic Signature Verification

- `rollops plugin info <name>` — prints full registry detail for one plugin:
  every published version with its per-platform artifacts and sha256 pins, plus
  cosign identity when the release is signed. Inspect before installing.
- `rollops plugin list` — lists installed plugins in the plugin directory with
  the sha256 each pins to (offline; matches what `install` printed). Recovers a
  pin for a spec without re-downloading. `--dir` overrides the directory.
- **Automatic cosign verification for marketplace installs.** When the registry
  declares a cosign identity for a release and the artifact carries signature
  material (a sigstore `bundle`, or detached `signature` + `certificate`),
  `plugin install <name>` now verifies the keyless signature automatically —
  the index's identity/issuer is the expected signer — before placing the
  binary. No flags required; a failed verification blocks the install. Manual
  `--cosign-*` flags still take precedence. A signed install now enforces both
  the sha256 pin and the publisher signature.

## v0.8.0 - Plugin Marketplace

- **Plugin marketplace.** A curated, version-controlled registry
  (`registry/plugins.json`) maps a plugin name to its published releases —
  per-platform download URLs, each with a sha256 pin, plus optional cosign
  identity. No service to run; the index is reviewed in Git.
  - `rollops plugin search [query]` lists published plugins, matching on name,
    description, or capability.
  - `rollops plugin install <name>` resolves the name to the current OS/arch
    artifact, downloads it, and enforces the registry's sha256 pin (a mismatch
    is rejected, never installed). `--version` pins a release; `--registry` /
    `ROLLOPS_PLUGIN_REGISTRY` override the index. Path and `https://` installs
    are unchanged.

## v0.7.0 - Plugin Install + Release Automation

- Release automation: a tag push (`v*`) now builds the cross-platform archives,
  checksums, and GitHub release via GoReleaser, replacing the manual flow. The
  release job rebuilds the embedded UI bundle and fails if it is stale.
- `rollops plugin install <path|https-url>`: fetches a plugin binary, optionally
  cosign-verifies it (`--cosign-key` or keyless `--cosign-identity/-issuer` with
  `--signature/--certificate/--bundle`), installs it into the plugin directory,
  and prints the sha256 to pin. Signature verified at install; sha256 pin
  enforced at launch.

## v0.6.0 - Manifest-Capability Plugins + Feature Flags

- **Manifest-capability plugin architecture** (nox-style). The typed
  `TargetPlugin` gRPC service is replaced by one generic `Plugin` service
  (`GetManifest` + `InvokeTool`). A plugin declares capabilities (named tool
  groups) and safety requirements (network hosts, file paths, env vars, risk
  class) in a manifest the host validates against a safety policy before
  invoking — capability-scoped trust instead of full daemon trust. New plugin
  kinds are new capabilities, not new services. **Breaking** vs the v0.5.0
  plugin protocol (pre-1.0, no external plugins yet).
  - `pkg/plugin` SDK: `NewManifest`/`NewServer`/`Serve`, plus typed wrappers
    `ServeTarget` (target capability) and `ServeFlagProvider` (featureflag
    capability). The host runtime moved to `internal/pluginhost`.
- **Feature-flag plugins** — the first new capability. A `featureFlags` spec
  block couples a rollout to a feature-flag provider plugin: the flag's rollout
  percentage tracks the canary traffic weight per step and/or settles at 100%
  on promote (`when: step | promote | both`). Best-effort; provider failures are
  audited, never abort the deploy. See `docs/feature-flags.md`.

## v0.5.1 - Dogfood Fixes + Hardening

- Drift is now asserted after a rollback too: the rolled-back-to manifest is
  the live baseline, so out-of-band changes after a rollback are flagged
  (previously only promoted targets were checked).
- Console banners distinguish failure modes: unauthorized (401/403, flagged
  immediately), live updates lost with last known state shown, and nothing
  loaded yet. The UI is served with `Cache-Control: no-cache` so an upgraded
  daemon never drives a stale cached bundle.
- Phase classification is centralized on `rollout.Phase` (`Settled`/`Active`/
  `Degraded` methods); drift reporting reads every target's observed
  fingerprint in one query instead of one per target.
- Plugin teardown hardening: plugins run in their own process group and are
  swept on close so forked children leave no orphans; plugin stderr is
  line-tagged and size-capped before reaching the daemon log; the handshake
  scanner has a bounded buffer.

## v0.5.0 - Target Plugin Runtime

- Target plugin process lifecycle: the new `plugin` target kind launches a
  sha256-pinned third-party plugin binary as a subprocess per engine
  operation — handshake on stdout, gRPC over a private unix socket, teardown
  via stdin close — and `pkg/plugin.Serve` is the public authoring toolkit
  (implement `pkg/target.Target`, call `Serve`). Unpinned or tampered
  binaries refuse to launch. The engine now releases targets holding runtime
  resources after every operation.

## v0.4.0 - Live Progressive Step Progress

- Live progressive step progress: the engine persists each health-gated
  step (index/total/weight, sqlite migration 0002) with a timeline note;
  the console shows a step bar on the target detail and a mini step
  indicator in the applications table while a rollout deploys.
- Step progress parity across surfaces: the gRPC StatusResponse carries
  the step fields and `rollops status` prints "steps 2/4 (50%)" in both
  one-shot and daemon mode.

## v0.3.0 - Console: Risk + Agent Attribution

- Console: real decisionkit risk scores in the apps table and detail view,
  agent/human/CI actor attribution icons, relative timestamps, clickable
  status facets, diff colouring, in-app rollback confirmation, keyboard
  shortcuts ("/" filter, Escape), attention count in the tab title, hidden-tab
  polling pause, and a stale-data banner.

## v0.2.0 - Email Notifications

- **Breaking:** replaced the Telegram notifier with email. Mail goes out
  either through a [briefkasten](https://github.com/klarlabs-studio/briefkasten)
  outbox (`ROLLOPS_BRIEFKASTEN_URL/TO/TOKEN` — durable, queued, retried via
  the `email.send` MCP tool) or direct SMTP (`ROLLOPS_SMTP_ADDR/FROM/TO` with
  optional `USER/PASS` PLAIN auth and STARTTLS). `ROLLOPS_TELEGRAM_*` is gone.
- Added `notify: ok|fail|skipped` check to `rollops doctor`: sends a test
  event to every configured notification channel so a bad SMTP server or
  webhook URL fails loudly at setup instead of being dropped at runtime.
- Added `docs/notifications.md` covering briefkasten, direct SMTP, and
  HMAC-signed webhook setup, event kinds, and best-effort delivery semantics.

## v0.1.0 - MVP OSS Core

- Implemented Rollops core engine: plan, apply, verify, promote, rollback,
  observe, schedule, approval gates, target locks, and structured plan/diff.
- Added infrastructure-agnostic targets for SSH, FTP, Kubernetes, and a hardened
  target plugin adapter with shared conformance tests.
- Added SQLite runtime store, Git watch-loop reconciliation, drift detection,
  progressive delivery, and observability-free rollback.
- Added trust surface: secrets provider, audit redaction, RBAC, agent guardrails,
  artifact verification, and authenticated CLI/HTTP/gRPC/MCP/UI interfaces.
- Added release polish: `doctor`, rollback across all operator surfaces, first-run
  docs, systemd packaging, example validation, RBAC docs, plugin docs, dashboard
  attention queue, Argo-like operator UI, `make release-check`, and release
  archives with checksums.
- Promoted metric-based rollout analysis to a stable optional Phase 2 feature,
  with config validation, Prometheus provider support, and UI timeline history
  notes for analysis pass/fail outcomes.
- Added optional historical risk scoring from recent target rollback history,
  including schema validation, CEL variables, and release docs.
- Added optional database rollback hooks for auto-rollback, with command
  validation, history notes, CLI status visibility, and UI timeline surfacing.
- Added SQLite-backed runtime leases for multi-instance coordination, covering
  shared target locks and reconcile leader election.
- Added optional OIDC-style bearer authentication and external group-to-RBAC
  mapping for API and UI deployments.
- Added image update policy validation and Git YAML writeback helpers for
  desired-state image automation.
- Added studio boundary and fleet dashboard contracts, plus optional feature
  flag and governance integration hooks.
- Renamed product surface cleanly to Rollops.
