# Command confinement

Rollops runs commands the **repo config names**: a smoke test, a database
migrate or rollback hook. In the documented "one repo per customer" model that
config is untrusted — so these controls bound what such a command can reach.

| Control | Env | Default |
| --- | --- | --- |
| Environment | `ROLLOPS_ALLOWED_ENV` | **on** — commands do not inherit the daemon environment |
| Command allowlist | `ROLLOPS_ALLOWED_COMMANDS` | off |
| Namespace allowlist | `ROLLOPS_ALLOWED_NAMESPACES` | off |
| Cluster confinement | `ROLLOPS_CONFINE_TARGET_CLUSTER` | off |

The last three are opt-in, so a single-tenant deployment behaves as before. The
environment control is the exception, and the reason is below.

## Why the environment control is default-on

The daemon's own environment holds every daemon secret: `ROLLOPS_MCP_TOKENS`,
`ROLLOPS_ADMIN_TOKEN`, `ROLLOPS_UI_PASSWORD`, `ROLLOPS_REGISTRY_TOKEN`, the OIDC
settings, and whatever the platform injects (`AWS_SECRET_ACCESS_KEY` and
friends). A subprocess inherits its parent's environment by default, so a smoke
test named by a watched repo could read all of it — `env | curl …` is the entire
exploit.

The plugin host has always confined its subprocess environment. Config-sourced
commands now do the same, through the same implementation.

A confined command receives only:

- `PATH`, `HOME`, `TMPDIR` — the essential set, no secrets
- anything named in `ROLLOPS_ALLOWED_ENV`

```sh
# a smoke test that needs the staging URL and a kubeconfig, nothing else
ROLLOPS_ALLOWED_ENV=SMOKE_BASE_URL,KUBECONFIG
```

To restore full inheritance — an explicit choice to hand config-sourced commands
the daemon's secrets:

```sh
ROLLOPS_ALLOWED_ENV=*
```

## Upgrading

**This is a behaviour change.** A smoke test or database hook that reads an
environment variable the daemon carries will stop seeing it after the upgrade.

Check your hooks for env-var reads before upgrading. The fix is to name the
variables in `ROLLOPS_ALLOWED_ENV`; `*` is available if you need the old
behaviour immediately, but prefer naming what you actually use — the point of
the control is that a hook cannot read a secret nobody meant to give it.

The startup log line reports the resolved policy:

```
rollopsd: multi-tenant confinement: commands=off namespaces=off cluster=off env=confined(+2)
```

`+2` is the number of operator-allowed variables beyond the essential set.

## What this does not cover

Confinement bounds the *environment* and *which binary* runs. It is not a
sandbox: an allow-listed command still executes as the daemon user with its
filesystem and network access. For untrusted tenants, run the daemon with a
dedicated low-privilege user, or isolate per-tenant daemons.
