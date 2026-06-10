# Session Capture — 2026-06-09 productization

## Summary

Rollops productization continued after P0 completion. The session focused on
making the product title clean, improving operator ergonomics, and aligning the
rollback capability across every surface.

## Changes captured

- **Canonical name cleanup**: Rollops is now the only product title/name in the
  current working tree. Legacy pre-Rollops text/path references were removed,
  including the SQLite migration comment and memory status wording.
- **UI dashboard filter**: dashboard now filters targets and rollouts by target
  ref, phase, fingerprints, rollout ID, strategy, and actor. Empty states now
  distinguish "no data" from "no matches".
- **UI asset hygiene**: embedded favicon avoids browser `/favicon.ico` Basic auth
  noise; asset tests catch stale CSS/bundle regressions.
- **CLI rollback**: `rollops rollback <target-ref>` added via the existing
  `cli.Operations` seam.
- **gRPC rollback**: `Rollback` RPC added to `proto/rollops/v1`, regenerated into
  `internal/grpcapi/rollopsv1`, authorized with `rollouts.rollback`, and wired
  into the daemon-mode CLI client adapter.
- **HTTP rollback**: `POST /v1/rollback` with body `{"target":"<target-ref>"}`
  added to the HTTP/JSON API, using `rollouts.rollback` scoped to the target.
- **Docs**: README quickstart now lists rollback in CLI and HTTP daemon surfaces.
- **Doctor command**: `rollops doctor [config.yaml]` added for release-readiness
  diagnostics. Local mode checks config + SQLite DB; daemon mode checks config +
  gRPC reachability/auth.
- **Systemd packaging**: added `rollopsd.service`, env template, installer, and
  deployment docs for the single-VPS path.
- **First-run path**: added `docs/first-run.md` with build, doctor, plan, daemon,
  Git watch, example validation, and VPS handoff steps.
- **Example guardrail**: `make examples-check` validates every shipped
  `examples/*.yaml` through config schema + semantic loading.
- **RBAC defaults/docs**: moved daemon bootstrap roles into
  `security.DefaultRBACPolicy()` and documented identity, permission, scope, and
  first-policy guidance in `docs/security-rbac.md`.
- **Plugin hardening**: plugin targets now fail closed on nil RPC and invalid
  health states; `docs/target-plugins.md` documents protocol and conformance
  expectations.
- **Operator workflow**: dashboard now has a compact attention queue for
  approvals, drifted targets, and active rollouts, reusing the existing action
  surface.
- **Release gate**: added `make release-check` and `docs/release-checklist.md`
  to turn the local validation flow into one repeatable command.
- **Version metadata**: added `rollops version`, `rollopsd version`, ldflags
  build metadata, and `CHANGELOG.md` for v0.1.0.
- **Roady normalization**: generated the requirement-level Roady coverage tasks
  expected by drift detection, approved the regenerated plan, and verified all
  100 tasks against the `make release-check` evidence.
- **Argo-like UI closure**: dashboard now presents a dense application list with
  health, sync/drift, derived operational risk, phase, desired/observed
  fingerprints, and last actor. Target detail now has desired/live/runtime/action
  summary cards and a rollout timeline alongside graph/list resources and diff.
- **Release archives**: added `make dist` / `make dist-check` for first-cut
  tarballs and SHA-256 checksums across Linux amd64/arm64 and Darwin arm64.

## Verification

- `make ui-typecheck`
- `make ui-build`
- `go test ./internal/ui`
- `make proto`
- `go test ./internal/cli`
- `go test ./internal/grpcapi`
- `go test ./internal/api`
- `go test ./internal/cli ./cmd/rollops`
- `bin/rollops doctor examples/rollout-config.example.yaml`
- `make examples-check`
- `go test ./internal/security ./cmd/rollopsd`
- `go test ./internal/target/plugin`
- `make ui-typecheck`
- `make ui-build`
- `go test ./internal/ui`
- `make package-check`
- `make release-check`
- `make dist-check`
- `bin/rollops version`
- `bin/rollopsd version`
- `scripts/install-systemd.sh --help`
- `make all`
- `roady drift detect`
- `roady status`
- Playwright smoke for `/ui`: dashboard rendered, filter visible, zero console
  warnings/errors.
- Playwright smoke for Argo-like `/ui`: dense application list rendered on
  desktop, target detail rendered summary cards/timeline/resource graph, mobile
  layout had no page-level horizontal overflow, zero current console errors.
- Repository-wide legacy-name scan returned no working-tree matches.

## Current roadmap view

- P0/Roady implementation is complete, verified, and drift-free.
- Near-term product/release polish items identified in this session are done.
- Near roadmap is closed: `/ui` now has the Argo-like daily operator shape:
  dense app/target list, resource tree/list, timeline/history, diff,
  sync/drift clarity, desired/live/runtime summary, and polished action flow.
- Deferred/P2: metric-based rollout analysis as a stable feature, DB rollback,
  historical-failure risk signal, multi-instance coordination, SSO, image
  automation, managed multi-customer layer.

## Notes for next session

- `memory/roadmap.md` is stale historical P0 build-order context. Prefer
  `memory/status.md` and this capture for current state.
- Worktree remains heavily dirty from the broader rename/productization effort;
  do not revert unrelated changes.
- `make all` was green at capture time.
