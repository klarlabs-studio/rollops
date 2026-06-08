# Rolloffs

*Rollout operations for the agentic web* · **Umbrella:** Klarlatz · **Status:** pre-MVP scaffold

A lightweight, infrastructure-agnostic rollout orchestration system — a leaner
alternative to ArgoCD/Flux built so **agents, not just humans, are first-class
operators**. Declare what you want deployed and where; Rolloffs handles the
rollout, the risk gate, the drift, and the rollback.

> Repo dir is `rollops`; the project/module name is **`rolloffs`**
> (`github.com/klarlabs/rolloffs`).

## Why

Kubernetes-grade GitOps is too heavy for a solo builder or small studio shipping
many products across heterogeneous infrastructure (one on K8s, one behind FTP,
one on a bare VPS). Rolloffs is the lean, declarative, agnostic control plane —
and it treats an autonomous agent as a native operator via MCP.

## Architecture (one line)

The **engine is a Go library** at the center; every interface — CLI, daemon,
MCP, UI — is a thin client over it. One-shot CLI runs the engine in-process
(no daemon); the daemon wraps the same engine behind gRPC + a REST gateway,
with the MCP server embedded. See `rolloffs-tdd.md`.

## Layout

```
cmd/rolloffs    CLI (one-shot in-process | gRPC client)
cmd/rolloffsd   daemon (reconciler + gRPC/REST + embedded MCP)
internal/
  engine        plan/apply/verify/promote/rollback/observe/schedule
  rollout       core runtime entities + statekit lifecycle
  reconcile     reconcile loop, drift detect/alert/reconcile
  risk          decision-kit risk gate (5 observability-free signals)
  target        target registry + first-party targets (k8s/ssh/ftp) + plugin
  store         Store interface + sqlite (default) / postgres / mnemos
  config        YAML + strict schema + CEL
  git           webhook+poll triggers, per-repo multi-tenancy
  secrets       SecretProvider (vault integration; never local)
  audit         bolt audit log with secret redaction
  mcp           mcp-go server (tools 1:1 to engine ops)
  api           gRPC + grpc-gateway REST; mTLS/signed-token auth
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
| Risk scoring | decision-kit |
| Audit / events | bolt |
| Agent interface | mcp-go |
| Bitemporal history (optional) | mnemos |

## Status

**P0 OSS core implemented** (37/37 planned tasks). Engine, risk gate, dependency
DAG, SSH/FTP/Kubernetes targets + plugin protocol, progressive delivery,
auto-rollback, secrets/audit/RBAC/guardrails/artifact-verification, Git webhook
+ watch-loop, and all four surfaces (CLI, gRPC + REST daemon, MCP, UI) plus
Prometheus self-observability. ~175 tests; `go test ./...` green. See
`memory/status.md` for the detailed map and remaining follow-ups.

## Quickstart

```sh
make build          # -> bin/rolloffs, bin/rolloffsd

# One-shot CLI (engine in-process, no daemon):
bin/rolloffs plan   examples/rollout-config.example.yaml
bin/rolloffs apply  examples/rollout-config.example.yaml
bin/rolloffs status <rollout-id>

# Daemon (HTTP :8080, gRPC :8090, UI behind basic auth):
make run-daemon
#   GET  /healthz /readyz /metrics
#   POST /v1/plan /v1/apply   (Authorization: Bearer <token>)
#   GET  /ui                  (basic auth: ROLLOFFS_UI_USER/PASSWORD)

# CLI in daemon mode (same commands, driven over gRPC):
ROLLOFFS_DAEMON=127.0.0.1:8090 ROLLOFFS_TOKEN=devtoken bin/rolloffs status <id>
```

Targets are configured per service in a `rolloffs.yaml`
(see `examples/rollout-config.example.yaml`); the daemon watches repos listed in
`ROLLOFFS_WATCH` (JSON: `[{"name","url","branch","path"}]`) and reconciles drift.

## License

Open-core. OSS core is single-tenant, self-hosted (engine, CLI, MCP, targets,
progressive delivery, drift, rollback, YAML/CEL, Git multi-tenancy, risk
scoring). Managed multi-customer orchestration is the studio/commercial layer.
