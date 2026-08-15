# First Run

This is the shortest path from a fresh checkout to a working local Rollops
operator loop.

## Build

```sh
make build
```

This creates:

- `bin/rollops` for one-shot CLI operations.
- `bin/rollopsd` for the daemon, API, MCP, and UI surfaces.

## Check Your Local Setup

```sh
bin/rollops doctor examples/rollout-config.example.yaml
```

In local mode, `doctor` validates the config and opens the configured SQLite
store. It does not require a daemon.

## Plan A Rollout

```sh
bin/rollops plan examples/rollout-config.example.yaml
```

The example config is intentionally small and uses the same YAML/CEL/schema path
as production configs. Keep desired state in Git; the SQLite store only holds
runtime state.

## Run The Daemon Locally

```sh
make run-daemon
```

Defaults:

- HTTP/API/UI: `127.0.0.1:8080`
- gRPC: `127.0.0.1:8090`
- API token: `devtoken`
- UI basic auth: `admin` / `dev`

Then point the CLI at the daemon:

```sh
ROLLOPS_DAEMON=127.0.0.1:8090 ROLLOPS_TOKEN=devtoken bin/rollops doctor
ROLLOPS_DAEMON=127.0.0.1:8090 ROLLOPS_TOKEN=devtoken bin/rollops status <rollout-id>
```

## Watch A Git Repo

The daemon watches desired state from Git repositories listed in
`ROLLOPS_WATCH`:

```sh
export ROLLOPS_WATCH='[{"name":"app","url":"https://example.com/app.git","branch":"main","path":"rollops.yaml"}]'
make run-daemon
```

`path` is the config file inside the repo. Rollops clones/pulls the repo, loads
that config, and reconciles drift against the selected target.

## Validate Shipped Examples

```sh
make examples-check
```

This loads every `examples/*.yaml` file through the same schema and semantic
validation used by runtime config loading.

## VPS Path

For a single-node VPS install with SQLite state and a systemd service, use:

```sh
make package-check
sudo scripts/install-systemd.sh
```

See `docs/deploy-systemd.md` for the full systemd checklist.

To let a named agent operate through MCP, see `docs/agent-operator.md`.
