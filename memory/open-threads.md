# Open threads — Rolloffs

## Blocking next work
- None blocking. Stack confirmed published + pinned (see decisions). Ready to start `t-config-schema`.

## Resolved
- **Stack module paths** (2026-06-08): all 7 published + pinned in go.mod, full build resolves.
  Vanity imports: `go.klarlabs.de/{statekit v1.8.0, axi v1.4.0, fortify v1.6.0, bolt v1.5.2, mcp v1.15.0, mnemos v0.19.0}`.
  decisionkit on GitHub: `github.com/felixgeelhaar/decisionkit v0.1.0` (risk gate uses `/risk` subpkg).
  Note: module names are `axi` and `mcp` (not `axi-go`/`mcp-go`). Blank-import anchor at `internal/stack/stack.go`.

## TDD §17 open items (revisit as it grows)
- Config schema v1 concrete YAML shape + CEL hook points + version field — decided in `config-model` task.
- gRPC plugin protocol contract + versioning — `target-contract`.
- Metric-based analysis interface (P2, Obvia seam) — deferred, do not build now.
- Multi-instance coordination (leader election) — studio/scale, deferred.
- UI act-vs-observe scope for v1 — decide in `ui-dashboard`.
- Concrete RBAC role/permission taxonomy — decide in `security-trust`.

## Decisions needing operator input
- Notification channel for approvals/failures (Telegram mentioned, P1).
- Default risk threshold + criticality weights — sensible defaults shipped, operator tunes via CEL.
