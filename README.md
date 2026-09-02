# Rollops

*Rollout operations for the agentic web* · **Umbrella:** Klarlatz · **Status:** Dogfooded OSS

A lightweight, infrastructure-agnostic rollout orchestration system — a leaner
alternative to ArgoCD/Flux built so **agents, not just humans, are first-class
operators**. Declare what you want deployed and where; Rollops handles the
rollout, the risk gate, the drift, and the rollback.

> Repo dir is `rollops`; the project/module name is **`rollops`**
> (`github.com/klarlabs/rollops`).

## Why

Kubernetes-grade GitOps is too heavy for a solo builder or small studio shipping
many products across heterogeneous infrastructure (one on K8s, one behind FTP,
one on a bare VPS). Rollops is the lean, declarative, agnostic control plane —
and it treats an autonomous agent as a native operator via MCP.

**Agent path:** the public proof is
[`docs/agent-walkthrough.md`](docs/agent-walkthrough.md) — Claude/Cursor → plan
with `needs_approval` + `recent_failures` → escalate or apply → history.
Operator knobs: [`docs/agent-operator.md`](docs/agent-operator.md). Mixed SSH +
Kubernetes: [`examples/hetero/`](examples/hetero/).

## Architecture (one line)

The **engine is a Go library** at the center; every interface — CLI, daemon,
MCP, UI — is a thin client over it. One-shot CLI runs the engine in-process
(no daemon); the daemon wraps the same engine behind authenticated HTTP/JSON
and gRPC surfaces, with the MCP server embedded. See `rollops-tdd.md`.

## Layout

```
cmd/rollops    CLI (one-shot in-process | gRPC client)
cmd/rollopsd   daemon (reconciler + HTTP/JSON + gRPC + embedded MCP)
internal/
  engine        plan/apply/verify/promote/rollback/observe/schedule
  rollout       core runtime entities + statekit lifecycle
  reconcile     reconcile loop, drift detect/alert/reconcile
  risk          blast-radius risk gate (observability-free + optional history)
  target        target registry + first-party targets (k8s/ssh/ftp) + plugin
  store         Store interface + sqlite (runtime state only)
  config        YAML + strict schema + CEL
  git           poll + HMAC GitHub webhook (poll is the safety net)
  secrets       SecretProvider (vault integration; never local)
  audit         bolt audit log with secret redaction
  mcp           mcp-go server (tools 1:1 to engine ops)
  api           HTTP/JSON + gRPC; bearer-token auth by default, TLS/mTLS at deployment
  security      RBAC, agent guardrails, kill-switch, attribution
pkg/target      public Target plugin contract
pkg/conformance shared conformance suite (every target must pass)
```

## Stack mapping

| Concern | Component |
|---|---|
| Rollout lifecycle | statekit |
| Step execution | axi-go |
| Resilience (retry/CB/rate/bulkhead) | fortify |
| Risk scoring | blast-radius gate (`internal/risk`) |
| Audit / events | bolt |
| Agent interface | mcp-go (embedded in the daemon) |

## Status

**P0 OSS core implemented** and dogfooded. Engine, risk gate, dependency DAG,
SSH/FTP/Kubernetes targets + plugin seam, progressive delivery,
observability-free auto-rollback, secrets/audit/RBAC/guardrails/artifact
verification, Git poll watch-loop, and the four product surfaces (CLI, HTTP/JSON
+ gRPC daemon, embedded MCP, UI) are present. Metric-based analysis is a stable
optional Phase 2 feature: set `ROLLOPS_ANALYSIS=1` to evaluate `spec.analysis`
(default off). Runtime state is SQLite; there is no Postgres or mnemos Store
backend. See `memory/status.md` for the detailed map and remaining follow-ups.

## Quickstart

```sh
make build          # -> bin/rollops, bin/rollopsd
bin/rollops version

# One-shot CLI (engine in-process, no daemon):
bin/rollops doctor examples/rollout-config.example.yaml
bin/rollops plan   examples/rollout-config.example.yaml
bin/rollops apply  examples/rollout-config.example.yaml
bin/rollops status <rollout-id>
bin/rollops verify <rollout-id>   # dry-run the post-deploy gate; changes nothing
bin/rollops promote <rollout-id>  # promote past the gate (--force to override)
bin/rollops rollback <target-ref>

# Daemon (HTTP :8080, gRPC :8090, UI behind basic auth):
make run-daemon
#   GET  /healthz /readyz /metrics
#   POST /v1/plan /v1/apply /v1/rollback   (Authorization: Bearer <token>)
#   GET  /ui                  (basic auth: ROLLOPS_UI_USER/PASSWORD)

# CLI in daemon mode (same plan/apply/status/rollback commands, driven over gRPC).
# Loopback is plaintext; set ROLLOPS_TLS_* to match a TLS daemon (docs/tls.md):
ROLLOPS_DAEMON=127.0.0.1:8090 ROLLOPS_TOKEN=devtoken bin/rollops doctor
ROLLOPS_DAEMON=127.0.0.1:8090 ROLLOPS_TOKEN=devtoken bin/rollops status <id>
```

Targets are configured per service in a `rollops.yaml`
(see `examples/rollout-config.example.yaml`); the daemon watches repos listed in
`ROLLOPS_WATCH` (JSON: `[{"name","url","branch","path"}]`) and reconciles drift.
For a guided local setup, see `docs/first-run.md`.

## Development

```sh
make all            # fmt-check, vet, tests, and Go binaries
make ui-typecheck   # TypeScript console type check
make ui-build       # rebuild embedded internal/ui/assets/app.js
make examples-check # validate all shipped rollout examples
make package-check  # validate release packaging helpers
make dist           # build release archives + checksums
make release-check  # full local release gate, including dist verification
make integration    # live SSH/FTP tests; K8s/Vault/cosign skip if absent
```

## VPS / systemd

The lean deployment path is a single `rollopsd` process with SQLite state:

```sh
make package-check
sudo scripts/install-systemd.sh
```

See `docs/deploy-systemd.md` for the unit, env file, and first-start checklist.
See `docs/agent-operator.md` for the **agent USP path** — Claude/Cursor MCP
client config, opt-in `agent-deploy`, plan with `risk_score` / `needs_approval` /
`recent_failures`, apply → canary → history attribution, optional cosign.
Proof narrative: `docs/agent-walkthrough.md`. Snippets: `examples/agent-loop/`.
Mixed targets: `examples/hetero/`.
See `docs/security-rbac.md` for the bootstrap roles and permission taxonomy.
See `docs/tls.md` for native TLS 1.3 + mTLS (zero-trust transport, cert-manager, client certs).
See `docs/oidc-auth.md` for OIDC bearer auth and external group-to-RBAC mapping.
See `docs/target-plugins.md` for the plugin architecture (manifest, capabilities, safety policy).
See `docs/kubernetes-sources.md` for the Kubernetes manifest sources (`manifestFrom` referenced path/kustomize/helm, plus the flat helm/kustomize/oci/bucket keys).
See `docs/prune-and-reap.md` for apply-time prune vs reclaiming resources when a RolloutConfig is deleted.
See `docs/feature-flags.md` for coupling a rollout to a feature-flag plugin.
See `docs/image-automation.md` for image update policy and Git writeback.
See `docs/studio-boundary.md` for the OSS/studio product boundary.
See `docs/optional-integrations.md` for feature flag and governance seams.
See `docs/notifications.md` for email (briefkasten/SMTP) and webhook notifications.
See `docs/external-governance.md` for requiring an external governor's approval before an apply (opt-in, fail-closed).
See `docs/multi-instance.md` for Store-backed target leases and reconcile
leader election.
See `docs/database-rollback.md` for optional database rollback hooks.
See `docs/risk-history.md` for the optional historical risk signal.
See `docs/metric-analysis.md` for the optional Prometheus/CEL post-deploy gate.
See `docs/release-checklist.md` before tagging an OSS release.

## License

The OSS core is [MIT](LICENSE)-licensed — single-tenant, self-hosted (engine,
CLI, MCP, targets, progressive delivery, drift, rollback, YAML/CEL, Git
multi-tenancy, risk scoring). Managed multi-customer orchestration is the
studio/commercial layer (open-core).
