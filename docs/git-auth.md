# Git Authentication (per-repo, least-privilege)

The reconcile watch authenticates to each repo independently. Credentials are
referenced by path in `watch.json` (`ROLLOPS_WATCH`), mounted from Secrets, never
stored by Rollops, and redacted from error logs. Pick the least-privileged option
that fits each repo.

| Field(s) | Scope | Rotation | Use when |
|----------|-------|----------|----------|
| `githubAppId` + `githubInstallationId` + `githubAppPrivateKeyFile` | installation's repos (per org / account) | automatic (~1h tokens) | A daemon writing image bumps to many repos, across orgs |
| `deployKeyPath` | one repo (SSH) | manual (long-lived key) | A single `git+ssh` repo |
| `token` / `tokenFile` | whatever the PAT grants | manual | Simplest; avoid sharing one PAT across repos/orgs |

A fine-grained PAT is scoped to **one** owner and a classic PAT is **broad** (all
the operator's repos) — both poor for a daemon that pushes to repos across
`klarlabs-studio` + a personal account. GitHub App installation tokens solve
this: install the App on each owner, and each repo references its installation.

## GitHub App (recommended)

1. Create a GitHub App (org or personal). Permissions: **Contents: Read & write**
   (Rollops reads config and pushes image-automation bumps). The App itself
   needs no webhook; repo webhooks (below) are optional and separate.
2. Generate a **private key** (PEM) and note the **App ID**.
3. Install the App on each owner, selecting only the repos Rollops manages. Note
   each **Installation ID** (`/orgs/<org>/installations`, or the install URL).
4. Mount the private key(s) as a Secret and reference them per repo:

```json
[
  {
    "name": "armada",
    "url": "https://github.com/klarlabs-studio/armada",
    "branch": "main",
    "path": ".rollops",
    "githubAppId": "123456",
    "githubInstallationId": "78901234",
    "githubAppPrivateKeyFile": "/etc/rollops/git/armada-app.pem"
  }
]
```

All three `githubApp*` fields are required together and take precedence over
`token`/`tokenFile`. Rollops mints an installation token on demand (App JWT →
`POST /app/installations/{id}/access_tokens`), caches it until ~60s before expiry,
and refreshes automatically. The token is resolved per git command and passed as
an `Authorization` header — never written to disk or into the remote URL.

For GitHub Enterprise, the API base is configurable in code
(`git.WithGitHubAPIBase`); the watch config currently targets github.com.

## Deploy keys (SSH)

Per-repo SSH key for a `git@github.com:owner/repo.git` URL:

```json
{ "name": "svc", "url": "git@github.com:acme/svc.git", "deployKeyPath": "/etc/rollops/git/svc_ed25519" }
```

Naturally single-repo and multi-org, but the key is long-lived (rotate manually).

## PAT (fallback)

```json
{ "name": "svc", "url": "https://github.com/acme/svc", "tokenFile": "/etc/rollops/git/token" }
```

`tokenFile` is read at startup; `token` is an inline value (operator-substituted).
Prefer a fine-grained PAT scoped to the single repo over a shared/classic one.

## GitHub webhook (optional, HMAC)

Poll is the reconcile safety net. To tick immediately on push, point a GitHub
**repository** webhook at the daemon:

- URL: `https://<rollopsd>/v1/hooks/github`
- Content type: `application/json`
- Secret: the same value as `ROLLOPS_WEBHOOK_SECRET`
- Events: at least `push` (ping is accepted)

The daemon verifies `X-Hub-Signature-256` and calls `watcher.Tick` for the
matching watched repo (`repository.full_name`). If the payload does not name a
known repo, every watched repo is ticked — still bounded by `ROLLOPS_WATCH`.
Invalid signatures are **401** and do not tick. If `ROLLOPS_WEBHOOK_SECRET` is
unset the route is **404**, so the listener is never an unauthenticated tick.

This is independent of GitHub App webhooks. The App used for clone/push does
not need a webhook subscription.

`ROLLOPS_WEBHOOK_SECRET` is also the optional HMAC key for *outbound* notify
webhooks (`ROLLOPS_WEBHOOK_URL`). Setting it for notify HMAC also opens the
inbound GitHub route; a caller still needs the secret to produce a valid
signature.

