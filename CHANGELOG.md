# Changelog

## Unreleased - Dogfood Fixes

- **Full drift verification** (`verification: full`). The default shallow
  stamped-checksum marker misses out-of-band field edits that leave the marker
  intact (e.g. `kubectl set image`). Full mode additionally diffs live state
  against the desired manifest in `plan`, reporting any divergence as drift
  (`live drifted from desired …`). Found while dogfooding on a live k3s cluster.
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
