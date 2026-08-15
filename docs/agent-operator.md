# Agent operator runbook

This is the **operator contract** (tokens, policy, TLS, tool list). For the
narrative proof strangers should follow, see
[agent-walkthrough](agent-walkthrough.md).

This page lets a **named agent** (Claude, Cursor, or any MCP client) drive
rollouts through the embedded MCP surface — the same loop you already dogfood.
Cluster/VPS wiring is operator work: set the env on the running `rollopsd` and
reload.

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
{"<token-a>": "claude"}
```

No tokens configured → every call is `403`. Rotation is `SIGHUP`. Details:
[MCP tokens](mcp-tokens.md). Snippet: [examples/agent-loop/mcp-tokens.example.json](../examples/agent-loop/mcp-tokens.example.json).

## 3. Grant deploy (opt-in)

Bootstrap `agent:*` stays plan+status. Bind **one** named agent to the built-in
`agent-deploy` role — plan, apply, rollback, status, promote/verify. Not freeze,
not approve.

```
ROLLOPS_POLICY_FILE=/etc/rollops/rbac-agent-deploy.yaml
```

```yaml
bindings:
  - subject: agent:claude
    roles: [agent-deploy]
```

Shipped snippet: [rbac-agent-deploy.yaml](rbac-agent-deploy.yaml) (edit the
subject to match your token name). Do **not** bind `agent:*` to `agent-deploy`.
`SIGHUP` reloads the policy. See [RBAC](security-rbac.md).

## 4. Point Claude or Cursor at MCP

HTTP MCP endpoint: `POST http://127.0.0.1:8091/mcp` (or `https://…` with TLS)
with `Authorization: Bearer <token>`.

**Cursor** — project or user MCP config (example under
`examples/agent-loop/cursor-mcp.example.json`):

```json
{
  "mcpServers": {
    "rollops": {
      "url": "http://127.0.0.1:8091/mcp",
      "headers": {
        "Authorization": "Bearer <token-a>"
      }
    }
  }
}
```

**Claude Desktop** — same URL + bearer header in its MCP server entry (shape
varies by Desktop version; use the HTTP/SSE transport your build documents).
The contract is identical: one named token → one agent identity → RBAC.

Never paste a live token into a chat transcript. Create the tokens file in your
own terminal.

## 5. Tool flow (the loop)

Observe tools are authorized as `rollouts.status`; apply still plans first.

1. **`rollouts.plan`** — YAML in. Returns `action`, `changed`, `summary`, and when
   `spec.risk` is configured: **`risk_score`**, **`needs_approval`**, **`sensitive`**,
   **`recent_failures`**, **`reason`**. Accepts a `RolloutSet` and expands it
   (same as the watcher).
2. **Escalate when `needs_approval` is true** — do not call apply. Cite
   `recent_failures` / `reason`. A human approves (CLI/UI/HTTP); agents cannot
   approve. Policy floor and freeze still bind regardless of score. Optional
   history signal: [risk history](risk-history.md). Walkthrough:
   [agent-walkthrough](agent-walkthrough.md).
3. **`rollouts.apply`** — deploys when the gate allows (or lands
   `awaiting_approval`). Needs `agent-deploy`. Refuses a `RolloutSet` — reconcile
   owns fan-out. Response includes `risk_score`. Pause/resume/abort an in-flight
   canary use this grant (`rollouts.pause` / `resume` / `abort`).
4. **`rollouts.status`** / **`list`** / **`history`** / **`drift`** / **`fleet`** —
   inspect. Status carries `risk_score` and actor; history shows who did what.
5. **`rollouts.verify`** then promote — dry-run the post-deploy gate; promote
   shares that permission. See [verify](verify.md).
6. **`rollouts.rollback`** — previous desired state for a target.

Freeze lift and approve stay human/admin.

## 6. Agent-grade provenance (opt-in)

When `ROLLOPS_COSIGN_KEY` is set, shared boot enables cosign **enforce** before
deploy (CLI and daemon). Without it, agents can still deploy — artifacts are
**not** provenance-checked. For an agent-grade bar, set the key on `rollopsd`
and pin signed images. See boot / security docs; example comment in
[examples/agent-loop/rollopsd.env.example](../examples/agent-loop/rollopsd.env.example).

## 7. Mixed infrastructure

Kubernetes is one target, not the universe. A side-by-side SSH + Kubernetes
pair lives under [examples/hetero/](../examples/hetero/). Plan both with the
same agent loop.

## Cluster

On the in-cluster Deployment this is env vars plus mounted files (tokens JSON,
policy YAML, optional cosign public key). Enabling it in the live fleet is an
operator change, not a Rollops release step.
