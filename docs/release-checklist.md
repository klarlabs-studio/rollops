# Release Checklist

Use this before tagging an OSS release.

## Local Gate

```sh
make release-check
```

This runs:

- UI TypeScript typecheck.
- UI bundle rebuild.
- Example config validation.
- Systemd/package helper validation.
- Cross-platform release archive generation and checksum verification.
- Go formatting check, vet, tests, and binary build.

The build injects git-derived version metadata into `bin/rollops version` and
`bin/rollopsd version`.

`make dist` writes `dist/rollops_<version>_<os>_<arch>.tar.gz` archives and
`dist/checksums.txt`. Each archive contains both binaries plus README,
changelog, first-run/security/plugin/systemd docs, examples, and systemd install
assets.

## Optional Live Gate

```sh
make integration
```

The integration harness exercises live SSH and FTP targets and skips optional
Kubernetes, Vault, and cosign checks when their local infrastructure is absent.

## Manual Smoke

```sh
bin/rollops doctor examples/rollout-config.example.yaml
make run-daemon
```

Then open `/ui`, check the dashboard attention queue, and run a daemon-mode
doctor probe:

```sh
ROLLOPS_DAEMON=127.0.0.1:8090 ROLLOPS_TOKEN=devtoken bin/rollops doctor
```

## Before Tagging

- Confirm `memory/status.md` captures the release state.
- Confirm `README.md` quickstart matches the shipped binaries and env names.
- Confirm `CHANGELOG.md` describes the release.
- Confirm `dist/checksums.txt` verifies with `cd dist && shasum -a 256 -c checksums.txt`.
- Confirm `docs/first-run.md`, `docs/deploy-systemd.md`,
  `docs/security-rbac.md`, `docs/oidc-auth.md`,
  `docs/target-plugins.md`, `docs/image-automation.md`,
  `docs/studio-boundary.md`, `docs/optional-integrations.md`,
  `docs/multi-instance.md`,
  `docs/database-rollback.md`, `docs/risk-history.md`, and
  `docs/metric-analysis.md` still match behavior.
