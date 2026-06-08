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

Greenfield scaffold: module, domain skeleton, and the two core contracts
(`pkg/target.Target`, `store.Store`) are in place and compile. Implementation
is tracked in Roady — `memory/status.md` has the current state.

## License

Open-core. OSS core is single-tenant, self-hosted (engine, CLI, MCP, targets,
progressive delivery, drift, rollback, YAML/CEL, Git multi-tenancy, risk
scoring). Managed multi-customer orchestration is the studio/commercial layer.
