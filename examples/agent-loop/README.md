# Agent loop examples

Placeholders only — never commit real tokens.

| File | Purpose |
|------|---------|
| `mcp-tokens.example.json` | Bearer token → agent name map for `ROLLOPS_MCP_TOKENS_FILE` |
| `rbac-agent-deploy.example.yaml` | Bind one named agent to `agent-deploy` |
| `cursor-mcp.example.json` | Cursor MCP server entry pointing at rollopsd |
| `rollopsd.env.example` | Daemon env knobs for MCP + optional cosign |

Copy beside your install, replace placeholders, mount/reload. Full contract:
[docs/agent-operator.md](../../docs/agent-operator.md).
