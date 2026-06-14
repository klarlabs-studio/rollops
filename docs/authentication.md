# Authentication

Every request to the gRPC/REST API is authenticated to an identity, then
authorized via RBAC (see `docs/rbac.md`). Authenticators are tried in order
(`ChainAuth`): bootstrap token first, then OIDC.

## Bootstrap token

`ROLLOPS_ADMIN_TOKEN` mints a `human:admin` identity (bound to the `admin` role).
Sent as `Authorization: Bearer <token>`. Intended for break-glass / CI, not
per-user access.

## OIDC (recommended for humans)

Set the issuer and audience, then choose a verification mode:

```bash
ROLLOPS_OIDC_ISSUER=https://idp.example
ROLLOPS_OIDC_AUDIENCE=rollops
```

**Asymmetric (real IdP) — recommended.** Verify RS256/384/512 or ES256/384/512
tokens against the provider's published JWKS. No shared secret; key rotation is
handled automatically (the key set is cached for an hour and refetched when an
unknown `kid` appears).

```bash
ROLLOPS_OIDC_JWKS_URL=https://idp.example/.well-known/jwks.json
# or discover jwks_uri from the issuer's well-known document:
ROLLOPS_OIDC_DISCOVER=1
```

**Shared secret (HS256).** For simple setups / testing:

```bash
ROLLOPS_OIDC_HS256_SECRET=<symmetric-secret>
```

Verification is stdlib-only (no JWT/JWKS dependency), so the daemon stays a
single static binary. Tokens are validated for signature, `iss`, `aud`, and
`exp`. Identity name is `preferred_username` → `email` → `sub`; `groups` claims
map to `group:<name>` RBAC subjects.

Group→role convenience bindings: `ROLLOPS_OIDC_ADMIN_GROUP` (default
`rollops-admins` → `admin`), `ROLLOPS_OIDC_AGENT_GROUP` (→ `agent`). Anything
finer-grained goes in `ROLLOPS_POLICY_FILE`.

## UI

The browser dashboard uses basic-auth (`ROLLOPS_UI_USER` / `ROLLOPS_UI_PASSWORD`)
exchanged for a session cookie, or an OIDC bearer token when OIDC is configured.

## Limitations

- OIDC verification covers signature + iss/aud/exp; it does not run the full
  authorization-code flow (Rollops consumes a bearer token an IdP/proxy issues).
- No SAML.
- JWKS is cached with a fixed TTL; there is no push-based rotation.
