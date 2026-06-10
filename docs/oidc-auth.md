# OIDC Authentication

Rollops accepts static bootstrap bearer tokens and optional OIDC-style bearer
JWTs at the same HTTP authentication boundary. Valid external identities are
mapped into the existing RBAC model; there is no second authorization system.

## Daemon Env

Set all three values to enable OIDC bearer validation:

```sh
ROLLOPS_OIDC_ISSUER=https://idp.example
ROLLOPS_OIDC_AUDIENCE=rollops
ROLLOPS_OIDC_HS256_SECRET=...
```

Rollops validates:

- compact JWT format
- `alg: HS256`
- HMAC signature
- `iss`
- `aud`
- `exp`

The identity name is selected from `preferred_username`, then `email`, then
`sub`. The `groups` claim is copied to `rollout.Identity.Groups`.

## RBAC Mapping

Groups bind to roles with the `group:<name>` identity key:

```go
policy.Bind("group:rollops-admins", security.RoleAdmin)
```

Daemon defaults:

- `ROLLOPS_OIDC_ADMIN_GROUP`, default `rollops-admins`, maps to `admin`.
- `ROLLOPS_OIDC_AGENT_GROUP`, when set, maps to `agent`.

Static `ROLLOPS_ADMIN_TOKEN` remains available for bootstrap and break-glass.

## UI

The UI accepts a valid OIDC bearer token before falling back to the existing
basic-auth/session-cookie flow. This supports deployments where an upstream IdP
or proxy injects the bearer token for `/ui` and `/ui/api/*`.
