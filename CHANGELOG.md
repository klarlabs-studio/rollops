# Changelog

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
