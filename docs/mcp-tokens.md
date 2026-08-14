# MCP tokens

Every MCP caller authenticates with a bearer token that resolves to a distinct
agent identity, so RBAC authorizes each caller as itself. The surface is
**fail-closed**: with no tokens configured, or a token that does not resolve,
the request is rejected (`403`) before any tool runs. There is no fallback
identity.

## Configuring

Preferred — a file, typically a mounted Secret:

```
ROLLOPS_MCP_TOKENS_FILE=/etc/rollops/mcp-tokens.json
```

```json
{
  "<token-a>": "nomi",
  "<token-b>": "deploy-bot"
}
```

Supported — the same JSON inline:

```
ROLLOPS_MCP_TOKENS={"<token-a>":"nomi","<token-b>":"deploy-bot"}
```

When both are set the **file wins** and the env var is ignored, logged at
startup so the live source is never ambiguous.

The map's values are agent *names*. What each agent may do is a separate
question, answered by the RBAC policy (`ROLLOPS_POLICY_FILE`) binding
`agent:<name>` to a role. Tokens are credentials; roles are authorization. Keep
them apart — rotating a credential should not touch a permission.

For the operator path that turns MCP on and grants one agent deploy, see
[Agent operator runbook](agent-operator.md).

## Why a file is preferred

- **Subprocess inheritance.** The daemon runs commands from repo config (smoke
  tests, database hooks). Those are confined (see
  [command confinement](command-confinement.md)), but anything in the daemon's
  environment is one misconfiguration away from a child process. File contents
  are not inherited.
- **Rotation without a restart.** A mounted Secret can be rewritten in place and
  re-read on `SIGHUP`. An env var needs a new pod.
- **Lower exposure.** Env vars show up in `/proc/<pid>/environ`, `docker
  inspect`, `kubectl describe pod` (when sourced from a ConfigMap), and crash
  dumps.

## Rotating

1. Add the new token alongside the old one and update the file.
2. `kill -HUP <rollopsd pid>` — or in Kubernetes, wait for the kubelet to
   propagate the updated Secret to the volume, then signal the pod.
3. Move callers onto the new token.
4. Remove the old token from the file and `SIGHUP` again.

The daemon logs each reload:

```
rollopsd: MCP tokens reloaded (2 token(s))
```

`SIGHUP` reloads the RBAC policy and the MCP tokens together. **A failed reload
keeps the current tokens** — a typo or a half-written file logs an error and
changes nothing, so a bad edit cannot lock every agent out mid-flight:

```
rollopsd: MCP token reload failed, keeping current: ...
```

Note the asymmetry with startup: at startup an unreadable token source is
fail-closed (no tokens, every caller rejected), because there is no known-good
state to keep.

## Kubernetes

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: rollopsd-mcp-tokens
  namespace: rollops-system
stringData:
  mcp-tokens.json: |
    {"<token-a>": "nomi"}
---
# in the rollopsd Deployment
        env:
          - name: ROLLOPS_MCP_TOKENS_FILE
            value: /etc/rollops/mcp-tokens.json
        volumeMounts:
          - name: mcp-tokens
            mountPath: /etc/rollops
            readOnly: true
      volumes:
        - name: mcp-tokens
          secret:
            secretName: rollopsd-mcp-tokens
```

Mounted Secret updates propagate to the volume automatically (subject to the
kubelet sync period), so rotation is: update the Secret, wait for propagation,
`SIGHUP`.
