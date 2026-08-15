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
| `rollouts.status` | Read rollout status/history/list/drift/fleet. |
| `rollouts.plan` | Compute a plan/diff from a rollout config. |
| `rollouts.apply` | Deploy desired state; pause, resume, and abort an in-flight canary (scoped to the target). |
| `rollouts.approve` | Approve a rollout held by the policy floor or risk gate. |
| `rollouts.promote` | Promote past the post-deploy gate; also authorizes dry-run `verify` (gates actually run). |
| `rollouts.rollback` | Roll back a target to the previous desired state. |
| `rollouts.schedule` | Create or fire scheduled rollout work. |
| `rollouts.freeze` | Engage or release the emergency freeze switch. |

Scopes can constrain a grant by `targetRef` and, where the caller supplies one,
`env`. Empty scope fields are wildcards. Current daemon API and gRPC calls
authorize against the rollout target ref.

## Bootstrap Defaults

`security.DefaultRBACPolicy()` installs three roles:

| Role | Binding | Grants |
|---|---|---|
| `admin` | `human:admin` | plan, apply, approve, promote (verify), rollback, status, schedule, freeze |
| `agent` | `agent:*` | plan, status |
| `agent-deploy` | *unbound* | plan, apply, rollback, status, promote (verify). **Not** freeze, **not** approve |

Agents are deliberately plan/status-only in the bootstrap policy. `agent-deploy`
is the documented opt-in (`agent:deploy`): bind a named agent to it in
`ROLLOPS_POLICY_FILE`. Do **not** bind `agent:*` to `agent-deploy`. `rollouts.verify`
shares `rollouts.promote` because the dry-run actually runs smoke/analysis.

## Recommended First Policy

For a single VPS install:

- Keep `ROLLOPS_ADMIN_TOKEN` long, random, and out of the repo.
- Put the daemon behind loopback or a private network by default.
- Use the UI only behind basic auth or a trusted reverse proxy.
- Let agents plan and inspect first.
- Add target-scoped agent apply grants only after dogfooding the target's
  rollback and conformance behavior.

Opt-in deploy for one named agent (the built-in `agent-deploy` role is already
defined; the file only binds it):

```yaml
# docs/rbac-agent-deploy.yaml — set ROLLOPS_POLICY_FILE to this path
bindings:
  - subject: agent:nomi
    roles: [agent-deploy]
```

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

Each toggle is audited (`ActionFreeze`). The state is persisted in the SQLite
Store and restored at boot — a restart does not lift the kill-switch. While
frozen, `Apply` returns `ErrFrozen`; promote/rollback are not blocked (recovery
must stay possible). Agents cannot lift the freeze (no `rollouts.freeze` on the
bootstrap `agent:*` role).
