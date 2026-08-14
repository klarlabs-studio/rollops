# RBAC

Every interface — gRPC, REST, MCP — authenticates the caller to an identity and
authorizes each operation through one policy (`internal/security`). Authorization
is **deny-by-default**: an identity with no matching grant is forbidden.

## Model

- **Permissions**: `rollouts.status`, `rollouts.plan`, `rollouts.apply`,
  `rollouts.approve` (gates approve **and** reject), `rollouts.promote`,
  `rollouts.rollback`, `rollouts.schedule`, `rollouts.freeze`.
- **Scope**: `{env, targetRef}`. An empty field on a *grant* is a wildcard. The
  request scope is derived from the config: `env` from `spec.target.env`,
  `targetRef` from `spec.target.ref`.
- **Role**: a named set of `{perm, scope}` grants.
- **Binding**: maps a subject to roles. Subjects: `human:<name>`, `agent:<name>`,
  `ci:<name>`, a `<kind>:*` wildcard, or `group:<name>` (from OIDC `groups`).

Bootstrap defaults (`DefaultRBACPolicy`): `admin` (all perms, bound to
`human:admin` when `ROLLOPS_ADMIN_TOKEN` is set), `agent` (plan + status,
bound to `agent:*`), and unbound `agent-deploy` (plan, apply, rollback, status,
promote/verify — not freeze, not approve). Bind a named agent to `agent-deploy`
in the policy file; do not bind `agent:*`.

## Operator policy file

Define custom roles and bindings without recompiling — point
`ROLLOPS_POLICY_FILE` at a YAML file, layered on top of the defaults (it may add
roles and bind to built-in roles like `admin`). A malformed file is **fatal**
(fail closed):

```yaml
roles:
  - name: backend-deployer
    grants:
      - perm: rollouts.apply
        env: staging            # only staging
      - perm: rollouts.rollback
        targetRef: demo/staging/api   # only this target
      - perm: rollouts.status   # any scope
bindings:
  - subject: group:backend-team
    roles: [backend-deployer]
  - subject: human:alice
    roles: [admin]
  - subject: agent:nomi
    roles: [agent-deploy]   # opt-in deploy; do not bind agent:*
```

Validation: every grant `perm` must be a known permission; every binding `role`
must exist (after this file's roles are applied). Unknown YAML fields are rejected.

## Env-scoped grants

For `env`-scoped grants to bind, tag the config's target:

```yaml
spec:
  target:
    kind: kubernetes
    ref: demo/staging/api
    criticality: medium
    env: staging
```

A grant with no `env` is a wildcard and still authorizes env-tagged requests, so
policies that don't use env scoping are unaffected.

## OIDC groups

When OIDC is configured, `groups` claims map to `group:<name>` subjects. Two
bootstrap env bindings exist for convenience:
`ROLLOPS_OIDC_ADMIN_GROUP` (default `rollops-admins` → `admin`) and
`ROLLOPS_OIDC_AGENT_GROUP` (→ `agent`). Anything finer-grained goes in the
policy file.

## Not RBAC

Guardrails (`internal/security/guardrails.go`) are a separate, non-bypassable
safety layer inside the engine — emergency freeze, agent rate-limit, and a policy
floor that forces human approval for critical/prod-schema changes regardless of
RBAC grants.

## Hot reload

Send `SIGHUP` to `rollopsd` to reload the policy file without a restart — the
policy is rebuilt (defaults + file + OIDC group binds) and swapped atomically,
so in-flight requests are unaffected. A malformed file is **rejected and the
current policy kept** (logged), so a typo can't lock everyone out.

```sh
kill -HUP $(pidof rollopsd)   # or: kubectl exec … -- kill -HUP 1
```

## Limitations

- Group bindings beyond the two bootstrap env vars require the policy file.
- No built-in policy-editing API/UI — the file is the source of truth.
