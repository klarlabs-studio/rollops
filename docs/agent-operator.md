# Agent operator runbook

This is the 15-minute path to let a named agent drive rollouts through the
embedded MCP surface. Cluster/VPS wiring is operator work: set the env on the
running `rollopsd` and reload. This page is the contract those knobs implement.

Default `agent:*` can **plan and status only**. Deploy is an opt-in grant.

## 1. Serve MCP

MCP is off until an address is set. Loopback for a same-host agent:

```
ROLLOPS_MCP_ADDR=127.0.0.1:8091
```

Non-loopback binds require TLS (`ROLLOPS_TLS_CERT` / `ROLLOPS_TLS_KEY`). With
`ROLLOPS_TLS_CLIENT_CA` set, MCP requires a client certificate as well as a
bearer token. See [TLS](tls.md).

Startup log when it is on:

```
rollopsd: MCP serving on 127.0.0.1:8091 (per-caller bearer auth, N token(s), TLS=… mTLS=…)
```

## 2. Tokens file

Each MCP caller presents `Authorization: Bearer <token>`. The token maps to an
agent **name**, not a role. Prefer a file (not an env var):

```
ROLLOPS_MCP_TOKENS_FILE=/etc/rollops/mcp-tokens.json
```

```json
{"<token-a>": "nomi"}
```

No tokens configured → every call is `403`. Rotation is `SIGHUP`. Details:
[MCP tokens](mcp-tokens.md).

## 3. Grant deploy (opt-in)

Bootstrap `agent:*` stays plan+status. Bind **one** named agent to the built-in
`agent-deploy` role — plan, apply, rollback, status, promote/verify. Not freeze,
not approve.

```
ROLLOPS_POLICY_FILE=/etc/rollops/rbac-agent-deploy.yaml
```

```yaml
bindings:
  - subject: agent:nomi
    roles: [agent-deploy]
```

Shipped snippet: [rbac-agent-deploy.yaml](rbac-agent-deploy.yaml). Do **not**
bind `agent:*` to `agent-deploy`. `SIGHUP` reloads the policy. See
[RBAC](security-rbac.md).

## 4. Tool flow

Endpoint: `POST http://127.0.0.1:8091/mcp` (or `https://…` with TLS) with the
bearer token. Observe tools are authorized as `rollouts.status`; apply still
plans first.

1. **`rollouts.plan`** — YAML in, summary out. Required before apply.
2. **`rollouts.status`** / **`rollouts.list`** / **`rollouts.history`** /
   **`rollouts.drift`** — inspect. `agent:*` can do this without the deploy grant.
3. **`rollouts.apply`** — deploys. Needs `agent-deploy` (or a narrower grant).
   Pause/resume/abort an in-flight canary use this same grant (`rollouts.pause` /
   `rollouts.resume` / `rollouts.abort`).
4. **`rollouts.verify`** — dry-run the post-deploy gate (health, smoke, analysis).
   Authorized as `rollouts.promote`; changes nothing. See [verify](verify.md).
5. **`rollouts.rollback`** — previous desired state for a target.

Promote after a passing verify is the same permission as verify. Freeze lift and
approve stay human/admin.

## Cluster

On the in-cluster Deployment this is three env vars plus two mounted files
(tokens JSON, policy YAML). Enabling it in the live fleet is an operator change,
not a Rollops release step.
