# RFC: Agent loop as product

Status: **Executed** — Phases A–D verified in Roady (157/157).
Date: 2026-08-15.
Companion: `.roady/spec.yaml` features `agent-risk-surface`, `agent-loop-publish`,
`agent-provenance-path`, `agent-hetero-demo`.

## Problem

Make it real + multi-cluster made the daemon honest and fleet-aware. You already
drive production through Claude + MCP. Competitors still cannot claim that loop.
What they *also* cannot copy yet is a **public, reproducible contract**: risk the
agent can see before apply, provenance on the agent path, attribution in history,
and one heterogeneous demo that is not "K8s-only GitOps."

Today `rollouts.plan` returns action/changed/summary only — no score, no
`needs_approval`. The engine gates on apply, but the agent cannot escalate from
the plan. Cosign and `risk.history` exist and stay opt-in; the agent runbook does
not make them part of the published loop. Examples are single-target; the vision
story is mixed infrastructure.

## Category (unchanged)

> The GitOps control plane for mixed infrastructure where agents and humans
> share one audited engine, and Kubernetes is a target, not the universe.

## Non-goals

- ApplicationSet matrix/git generators, Jsonnet, app-of-apps
- Host agent, Postgres/mnemos, Studio billing
- New MCP verbs beyond surfacing risk/attribution on existing tools
- Forcing cosign or risk.history on every install (opt-in stays; docs name the
  agent-grade bar)
- Obvia metric analysis as default (Phase 2 seam stays opt-in)

## Honesty table

| Claim / gap today | Intended after this program |
|---|---|
| Agent plans without a risk number | `plan` returns score + needs_approval (+ sensitive) when risk is configured |
| Apply/status hide score from MCP | Apply/status carry `risk_score`; history already has actor |
| Runbook stops at knobs | Runbook is Claude/Cursor MCP client → plan → risk escalate → apply → canary → history |
| Cosign is a daemon env footnote | Agent-grade path documents `ROLLOPS_COSIGN_KEY` enforce |
| Heterogeneous story is prose | Example pair: SSH + Kubernetes under one narrative |

## Phases

### Phase A — Risk on the agent surface

**Goal:** an agent can refuse or escalate *from plan*, not only discover a gate
on apply.

- `Engine.Plan` attaches risk when `spec.risk` is configured (`EvaluateRisk` +
  `RiskFromConfig`), including history when `risk.history` is set.
- MCP / HTTP / gRPC plan responses expose `risk_score`, `needs_approval`,
  `sensitive`. CLI plan prints them.
- MCP / HTTP / gRPC status (and apply response) expose `risk_score` (and actor
  on status where the rollout carries initiator).
- Tests: table-driven; plan with threshold sees needs_approval for schema/prod;
  plan without risk config leaves fields zero/false.

### Phase B — Publish the loop

**Goal:** a stranger with Claude or Cursor can reproduce your dogfood path.

- Expand `docs/agent-operator.md`: MCP client JSON (Cursor / Claude Desktop),
  full tool flow including risk escalate, canary pause/abort, history
  attribution, link risk-history + cosign + TLS.
- README points at this as the agent USP path (not a buried link).
- Snippet under `examples/agent-loop/` for tokens + policy + client config
  (no secrets).

### Phase C — Provenance on the agent path

**Goal:** agent-grade deploy means signed artifacts when configured.

- Document `ROLLOPS_COSIGN_KEY` → `VerifyEnforce` in the agent runbook and
  example env comments.
- No behaviour change when unset (unsigned fleets keep working).
- Doctor/runbook state clearly: without the key, agents deploy without
  artifact verify.

### Phase D — Heterogeneous demo

**Goal:** one example directory that is the vision in YAML.

- `examples/hetero/`: one SSH rollout + one Kubernetes rollout, shared labels,
  short README pointing at `rollops doctor` / plan on both.
- Linked from agent-operator and first-run.

## Done when

1. Roady tasks for the four features verified.
2. An MCP `plan` against `examples/rollout-config.example.yaml` returns a
   non-zero risk story when risk is configured.
3. Agent-operator is the public Claude/Cursor path.
4. Hetero examples load under `make` examples-check.

Not done when: more Argo checkboxes. Stop.
