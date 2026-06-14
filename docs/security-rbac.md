# Security And RBAC

Rollops treats every interface as privileged. CLI daemon mode, HTTP/JSON, gRPC,
MCP, and the UI must resolve to an identity before they can operate on rollout
state.

## Identity

Identities use the same shape everywhere:

- `human:<name>` for people.
- `agent:<name>` for autonomous agents.
- `ci:<name>` for CI systems.

The daemon bootstrap path accepts one admin bearer token through
`ROLLOPS_ADMIN_TOKEN` and maps it to `human:admin`. Production deployments should
put TLS/mTLS, a reverse proxy, or an external identity provider in front of the
daemon, but the Rollops authorization check still happens inside the daemon.

## Permissions

| Permission | Meaning |
|---|---|
| `rollouts.status` | Read rollout status/history. |
| `rollouts.plan` | Compute a plan/diff from a rollout config. |
| `rollouts.apply` | Deploy desired state from a rollout config. |
| `rollouts.approve` | Approve a rollout held by the policy floor or risk gate. |
| `rollouts.rollback` | Roll back a target to the previous desired state. |
| `rollouts.schedule` | Create or fire scheduled rollout work. |
| `rollouts.freeze` | Engage or release the emergency freeze switch. |

Scopes can constrain a grant by `targetRef` and, where the caller supplies one,
`env`. Empty scope fields are wildcards. Current daemon API and gRPC calls
authorize against the rollout target ref.

## Bootstrap Defaults

`security.DefaultRBACPolicy()` installs two roles:

| Role | Binding | Grants |
|---|---|---|
| `admin` | `human:admin` | plan, apply, approve, rollback, status, schedule, freeze |
| `agent` | `agent:*` | plan, status |

Agents are deliberately plan/status-only in the bootstrap policy. Granting an
agent deploy or rollback rights is an operator decision and should be scoped to
the narrowest target refs possible.

## Recommended First Policy

For a single VPS install:

- Keep `ROLLOPS_ADMIN_TOKEN` long, random, and out of the repo.
- Put the daemon behind loopback or a private network by default.
- Use the UI only behind basic auth or a trusted reverse proxy.
- Let agents plan and inspect first.
- Add target-scoped agent apply grants only after dogfooding the target's
  rollback and conformance behavior.

Example target-scoped extension in Go:

```go
policy := security.DefaultRBACPolicy()
policy.DefineRole(security.Role{Name: "agent-staging-api", Grants: []security.Grant{
	{Perm: security.PermPlan, Scope: security.Scope{TargetRef: "pet-medical/staging/api"}},
	{Perm: security.PermApply, Scope: security.Scope{TargetRef: "pet-medical/staging/api"}},
	{Perm: security.PermStatus},
}})
policy.Bind("agent:nomi", "agent-staging-api")
```

That still does not bypass guardrails. The policy floor, emergency freeze,
artifact verification, rate limits, audit attribution, and rollback checks remain
inside the engine path.

## Emergency freeze

The kill-switch (blocks every apply) is toggled through any interface, gated by
`rollouts.freeze`:

- CLI: `rollops freeze [reason]` / `rollops unfreeze`
- gRPC: `Freeze(active, reason)`; REST: `POST /v1/freeze {"active":true,"reason":"…"}`
- MCP: `rollouts.freeze` tool; UI: the freeze toggle on the dashboard

Each toggle is audited (`ActionFreeze`). The state is held in memory — it does
**not** survive a daemon restart (re-engage after a restart if an incident is
still open). While frozen, `Apply` returns `ErrFrozen`; promote/rollback are not
blocked (recovery must stay possible).
