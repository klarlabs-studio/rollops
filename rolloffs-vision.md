# Rolloffs — Vision Document

*Rollout operations for the agentic web*
**Umbrella:** Klarlatz · **Status:** Concept / pre-MVP

---

## One-liner

Rolloffs is a lightweight, infrastructure-agnostic rollout orchestration system. It is a modern, far leaner alternative to ArgoCD and Flux — built so that **agents, not just humans, are first-class operators**. You declare what you want deployed and where; Rolloffs handles the rollout, the risk gate, the drift, and the rollback.

---

## Problem Statement

GitOps and progressive-delivery tooling is overwhelmingly Kubernetes-first and operationally heavy. ArgoCD and Flux are powerful but assume your deployment target is a cluster and your operator is a human clicking through a UI or writing pipeline YAML. (Direct experience at Snyk: Argo was painful.) Meanwhile feature-flag platforms (LaunchDarkly, Flagsmith, Statsig) only operate at the application layer, and procedural tools (Ansible) are not declarative or continuously reconciling.

For a **solopreneur or small studio shipping many products across heterogeneous infrastructure** — one product on Kubernetes, another behind an FTP step, another on a bare Hetzner VPS — there is no lean, declarative, agnostic control plane. And nothing treats an autonomous agent as a native operator.

---

## Vision

Rolloffs becomes the **nervous system of the studio**: a single declarative control plane that manages deployments across every product and, as the studio scales to serving many customers, across every customer's infrastructure — without the operator drowning in complexity.

It is dogfooded internally first across the existing product portfolio, then open-sourced publicly (the same path the consumer products take) for the audience that is currently underserved: **solopreneurs and small teams for whom Kubernetes-grade GitOps is far too big**.

Brand DNA carries through: **Smart, Präzise, Wertig, Verlässlich.**

---

## Goals

1. **Make deployment shifting trivial at studio scale** — manage rollouts across dozens of products/customers from one declarative system, regardless of underlying infrastructure.
2. **Make agents native operators** — an agent can decide, schedule, execute, and roll back deployments safely through MCP, not just observe.
3. **Stay radically lean** — low resource consumption, no Kubernetes dependency, runs comfortably on minimal infrastructure (e.g. a Hetzner VPS).
4. **Be infrastructure-agnostic by design** — Kubernetes, VMs, FTP, or anything else, through pluggable targets.
5. **Earn trust through traceability** — every decision, approval, and execution is auditable across multiple layers.

---

## Non-Goals (for now)

- **Not another Kubernetes / orchestrator.** Rolloffs configures and drives deployment *through* existing infrastructure; it does not replace the runtime.
- **Not coupled to Obvia.** Observability integration is deliberately left out of the MVP. Technology may be reused later; the dependency is not baked in yet.
- **Not a feature-flag platform.** Application-level flags (LaunchDarkly/Flagsmith/Statsig) stay a separate concern. Integration hooks are a future consideration, not v1.
- **Not tightly coupled to Relicta.** Risk evaluation uses decision-kit directly; a deeper Relicta change-governance binding is optional and deferred.
- **Not enterprise-first.** The design center is the solo builder and small team, not large-org governance theater.

---

## Target Users & Personas

- **The studio operator (you).** Manages many products/customers; needs shifting to be effortless and safe.
- **The autonomous agent (Nomi and peers).** Detects a meaningful change, evaluates risk, executes or schedules a rollout, rolls back if needed — via MCP.
- **The solopreneur / small team (OSS audience).** Wants progressive, safe rollouts without standing up a cluster or learning Argo.

---

## How it fits the stack

- **Layer:** Developer tooling (alongside scout, coverctl, tokenops), under the **Klarlatz** umbrella.
- **decision-kit** — calculates the blast-radius / risk score that drives the approval gate.
- **bolt** — structured, compliance-grade audit logging across layers.
- **MCP (mcp-go)** — the agent interface; how Nomi and other agents operate Rolloffs natively.
- Dogfood targets at launch: **Armada, Obvia, Pet Medical, Brotwerk, IRI.**

---

## User Stories

**Studio operator**
- As a studio operator, I want each customer and service to own its own repo and config so that deployments are isolated by Git structure and easy to replicate.
- As a studio operator, I want to schedule a rollout for a specific time so that changes land in a controlled window.
- As a studio operator, I want a UI that shows the live state of every rollout so that I can see what's deployed where at a glance.

**Agent**
- As an agent, I want to drive deployments through MCP so that I can roll out a meaningful change autonomously once it clears the risk gate.
- As an agent, I want to roll back a deployment when something goes wrong so that I can self-correct without waiting for a human.
- As an agent, I want a risk score for a proposed change so that I escalate to a human only when it exceeds the threshold or is flagged sensitive.

**Solopreneur (OSS)**
- As a solo developer, I want progressive rollouts (canary/blue-green) on a plain VPS so that I get safe releases without a Kubernetes cluster.
- As a solo developer, I want to write my deployment config declaratively so that the desired state lives in Git and is continuously reconciled.

---

## Requirements

### Must-Have (P0)

- **Declarative core engine** — desired-state model, infrastructure-agnostic, with **pluggable deployment targets** (Kubernetes, VMs, FTP, etc.).
- **Config format: declarative YAML + strict schema, CEL for logic.** YAML is the surface format — fluently written by both humans and agents — backed by a published schema and Go-side validation that rejects malformed config loudly. Conditional logic (risk thresholds, gate conditions) uses embedded CEL (Common Expression Language) rather than a bespoke DSL. Relicta's governance DSL stays separate; the two concerns evolve independently.
- **Three interfaces** — CLI (automation/humans), UI (visibility), **MCP (agents)**.
- **Progressive delivery** — canary, blue-green, and rolling strategies with configurable traffic shifting.
- **Risk-based approval gate** — decision-kit computes a blast-radius risk score from five observability-free signals: **target criticality** (operator-configured weight per service), **environment** (prod > staging > dev), **change type** (config < code < schema/DB migration), **blast radius** (count of downstream dependents from the dependency graph), and **rollout strategy** (full cutover riskier than a small canary). Below threshold proceeds automatically; above threshold or sensitive-flagged requires a **single human approval** (approve / reject / block). Sensible safe defaults; no multi-stage approval chains in v1.
- **Drift handling** — continuously **detect, alert, and reconcile** actual vs. declared state.
- **Rollback strategies** — manual, automatic, and agent-driven. The v1 auto-rollback signal is observability-free: an operator-defined **health check** (HTTP / TCP / command exit), a **post-deploy smoke test** ("run this, expect exit 0"), or a **rollout step error / timeout**. Any failing fires the rollback. Metric-based analysis is deferred to Phase 2.
- **Audit & compliance logging** — built in from day one via bolt; structured and traceable across multiple layers so anyone can understand what happened and why.
- **Multi-tenancy via Git** — each customer/service has its own repo and config; isolation through repo structure (separation of concerns), easy to replicate. This per-repo mechanism is part of the OSS core; managed coordination *across* many customers is the studio/commercial layer (see Open-Core Boundary).
- **Secrets handling via best practice** — integrate with established vaults (e.g. HashiCorp Vault, cloud secret managers); never store secrets locally.
- **Scheduled deployments** — queue a rollout for a specific future time (human or agent initiated).
- **Dependency ordering** — support both fully independent service deployments and explicit dependency chains (A completes before B).
- **Environment promotion** — support both staged promotion (dev → staging → prod) and independent per-environment configs. Everything configurable.

### Nice-to-Have (P1)

- Historical failure-rate signal feeding the risk score (once enough run data exists).
- Database rollback support (schema/data) following best practices.
- Notification integrations for approvals and failures (e.g. Telegram).
- Richer UI dashboards for rollout history and state.

### Future Considerations (P2)

- Observability integration (potentially reusing Obvia technology).
- Feature-flag platform integrations (LaunchDarkly / Flagsmith) for a combined release workflow.
- Deeper Relicta change-governance binding.

---

## Guiding Principles

- **Everything is configurable.** Thresholds, strategies, promotion paths, approval gates — the operator decides.
- **Agnostic over opinionated infrastructure.** The target is a plugin, never an assumption.
- **Lean as a forcing function.** Every feature must justify its weight; low resource consumption is a feature.
- **Agents and humans, same surface.** CLI, UI, and MCP are peers.
- **Trust by default.** Safe defaults, human-in-the-loop on sensitive changes, full auditability.

---

## Open-Core Boundary

- **OSS core (single-tenant, self-hosted):** engine, CLI, MCP server, target plugins, progressive delivery, drift reconciliation, rollback, YAML/CEL config, Git-based multi-tenancy, and decision-kit risk scoring (decision-kit and bolt are already open). Anyone can run Rolloffs against as many repos as they want, for free.
- **Studio / commercial layer:** managed coordination *at scale* — one pane of glass orchestrating many customers, hosted dashboard, tenancy controls, billing. This is the "manage fifty customers easily" value, following the same open-core split as Argo/Codefresh and Flux/Weave.

---

## Resolved Decisions

All initial open questions are resolved (reflected in Requirements above):

- **Config format** → declarative YAML + strict schema + Go validation; CEL for conditional logic; no custom DSL; relicta's DSL kept separate.
- **Risk model inputs** → weighted score over target criticality, environment, change type, blast radius, and rollout strategy — all computable without observability. Historical failure rate added as P1 once data exists.
- **Auto-rollback triggers** → health check, post-deploy smoke test, or step error/timeout (observability-free). Metric-based analysis → Phase 2.
- **OSS vs. studio boundary** → open-core; per-repo tool is OSS, managed multi-customer orchestration is the commercial layer.
- **Approval depth** → single configurable gate (auto / one approver / block); multi-stage chains → P2 only on real demand.

---

## Phasing

1. **Phase 0 — Dogfood.** Internal use across the studio's products; prove the core engine, MCP interface, and risk gate on real rollouts.
2. **Phase 1 — Public OSS.** Open-source release for solopreneurs and small teams; CLI + UI + MCP, progressive delivery, drift reconciliation, Git-based multi-tenancy.
3. **Phase 2 — Ecosystem.** Metric-based rollout analysis (the natural Obvia seam), optional integrations (observability, feature flags, deeper governance), database rollback, richer dashboards, and the managed multi-customer studio layer.
