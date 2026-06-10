# Open threads — Rollops

## Blocking next work
- None blocking. P0/Roady and near-roadmap UI/release polish are complete; next
  work is a first release cut or P2 selection.

## Resolved
- **Stack module paths** (2026-06-08): all 7 published + pinned in go.mod, full build resolves.
  Vanity imports: `go.klarlabs.de/{statekit v1.8.0, axi v1.4.0, fortify v1.6.0, bolt v1.5.2, mcp v1.15.0, mnemos v0.19.0}`.
  decisionkit on GitHub: `github.com/felixgeelhaar/decisionkit v0.1.0` (risk gate uses `/risk` subpkg).
  Note: module names are `axi` and `mcp` (not `axi-go`/`mcp-go`). Blank-import anchor at `internal/stack/stack.go`.

## TDD §17 open items (revisit as it grows)
- Config schema v1 concrete YAML shape + CEL hook points + version field — decided and implemented.
- gRPC plugin protocol contract + versioning — implemented; adapter hardened for nil RPC and invalid health states; process lifecycle/distribution remains future work before broad third-party support.
- Metric-based analysis interface (P2, Obvia seam) — experimental opt-in seam exists; keep disabled in v1 defaults.
- Multi-instance coordination (leader election) — studio/scale, deferred.
- UI act-vs-observe scope for v1 — closed. Read-and-act dashboard now has
  filter, attention queue, dense application list, derived health/sync/risk,
  detail summary, resource graph/list, diff, timeline, rollback, sync, approve,
  and reject. Next UI work should come from dogfood findings or P2/studio scope.
- Concrete RBAC role/permission taxonomy — first taxonomy exists; refine from dogfood evidence.

## Decisions needing operator input
- Notification channel for approvals/failures (Telegram mentioned, P1).
- Default risk threshold + criticality weights — sensible defaults shipped, operator tunes via CEL.
- Release polish priority: `doctor` command, install/systemd packaging, first-run docs/examples, RBAC docs/defaults, plugin adapter hardening, dashboard workflow refinement, release-check aggregation, version metadata, changelog, and Roady drift cleanup are done.
