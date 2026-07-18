# Deploy Rollops With Systemd

This is the lean single-VPS deployment path: one `rollopsd` process, SQLite
runtime state, journald logs, and optional UI/MCP/gRPC surfaces behind host or
reverse-proxy authentication.

## Install

Build and install binaries, the unit, and an env template:

```sh
make package-check
sudo scripts/install-systemd.sh
```

The installer writes:

- `/usr/local/bin/rollops`
- `/usr/local/bin/rollopsd`
- `/etc/systemd/system/rollopsd.service`
- `/etc/rollops/rollopsd.env` if it does not exist
- `/var/lib/rollops` for SQLite state and watched repo clones

## Configure

Edit `/etc/rollops/rollopsd.env` before starting:

```sh
sudoedit /etc/rollops/rollopsd.env
```

Set at least:

- `ROLLOPS_ADMIN_TOKEN`
- `ROLLOPS_UI_PASSWORD` if you want `/ui` enabled
- `ROLLOPS_WATCH` with the repos to reconcile

Generate tokens with:

```sh
openssl rand -hex 32
```

## Start

```sh
sudo systemctl start rollopsd
sudo systemctl status rollopsd
journalctl -u rollopsd -f
```

Check daemon connectivity from the CLI:

```sh
ROLLOPS_DAEMON=127.0.0.1:8090 ROLLOPS_TOKEN=<admin-token> rollops doctor
```

## Defaults

- HTTP binds to `127.0.0.1:8080`.
- gRPC binds to `127.0.0.1:8090`.
- SQLite lives at `/var/lib/rollops/rollops.db`.
- Watched repo checkouts live under `/var/lib/rollops/repos`.
- `/ui` is disabled until `ROLLOPS_UI_PASSWORD` is set.
- MCP is disabled until `ROLLOPS_MCP_ADDR` is set, and is fail-closed once
  enabled: it rejects every call until `ROLLOPS_MCP_TOKENS` (a JSON
  `{token: agent-name}` map) is set and callers present `Authorization: Bearer
  <token>`. Each token resolves to a distinct identity for per-caller RBAC.

Put TLS, mTLS, SSO, or public routing at the reverse proxy/host boundary. The
daemon itself still requires tokens/basic auth for its interfaces.
