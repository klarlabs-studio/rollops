# Agent walkthrough — proof of the loop

This is the public proof that Rollops treats agents as peer operators. It is
what Claude or Cursor should do against a running `rollopsd` with MCP enabled.
Operator knobs (tokens, policy, TLS) are in [agent-operator](agent-operator.md);
snippets live under [`examples/agent-loop/`](../examples/agent-loop/).

## 0. Prerequisites

- `rollopsd` with `ROLLOPS_MCP_ADDR`, tokens file, and `agent-deploy` for one
  named agent (e.g. `claude`).
- A config with **risk + history** — use
  [`examples/hetero/ssh.yaml`](../examples/hetero/ssh.yaml) or
  [`examples/rollout-config.example.yaml`](../examples/rollout-config.example.yaml).
- Optional agent-grade: `ROLLOPS_COSIGN_KEY` set (enforce). Unset is honest:
  agents can still deploy without artifact verify.

## 1. Plan — see risk before you touch prod

Call `rollouts.plan` with the config YAML. When `spec.risk` is set the response
includes a citeable gate:

```json
{
  "action": "update",
  "changed": true,
  "summary": "demo/prod/api [ssh]: update — …",
  "risk_score": 0.42,
  "needs_approval": true,
  "sensitive": true,
  "recent_failures": 2,
  "reason": "sensitive: flagged by policy (score 0.42)"
}
```

`recent_failures` is the count of `rolled-back` records inside
`risk.history.lookback`. Agents escalate because of **evidence**, not a mystery
boolean. See [risk history](risk-history.md).

## 2. Escalate — do not apply

If `needs_approval` is true, **stop**. Do not call `rollouts.apply`. A human
approves via CLI / UI / HTTP (`rollops approve <id>` after an apply that landed
`awaiting_approval`, or refuse the change). Agents cannot approve; freeze and
policy floor still bind.

## 3. Apply only when clear

When `needs_approval` is false (or a human has approved an awaiting rollout),
`rollouts.apply` deploys. Response carries `risk_score`. Pause / resume / abort
an in-flight canary with the same grant.

## 4. Verify, then promote

`rollouts.verify` dry-runs the post-deploy gate. Promote shares that permission.
See [verify](verify.md).

## 5. Prove attribution

`rollouts.history` for the target shows `actor_kind` / `actor_name` (e.g.
`agent` / `claude`). That is the audit trail competitors cannot claim for MCP
natives.

## 6. Mixed infrastructure

Same agent, same daemon: plan both halves of [`examples/hetero/`](../examples/hetero/)
(SSH + Kubernetes). Kubernetes is a target, not the universe.

## Done when

A stranger can follow this page once, see `recent_failures` on plan, refuse an
unsafe apply, and read agent attribution in history — without reading the TDD.
