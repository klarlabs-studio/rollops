# Decisions — Rollops

Append-only. Superseded entries get `→ superseded [date]`, never deleted.

## 2026-06-08 — Scaffold & plan

- **Module path** = `github.com/klarlabs/rollops`. Repo dir is `rollops`; project name is `rollops` (matches all docs). Kept the mismatch; noted in README.
- **Packages by domain, not layer** (per klarlabs best-practice). `internal/` for engine guts, `pkg/` for the public Target plugin contract + conformance suite (community plugins import it).
- **Target contract is public** (`pkg/target`) so third-party gRPC plugins can implement it. Store stays `internal` (no external implementers expected in OSS core).
- **Domain entities live in `internal/rollout`** to avoid import cycles (Store imports rollout + pkg/target).
- **Roady plan = 1:1 task per requirement** (63 tasks). Inter-task deps NOT wired in Roady; build order documented in `roadmap.md` instead. Plan approved to unblock execution.
- **Build order: contracts → SQLite → engine → lifecycle/gate → targets → reconcile/rollback → interfaces → security → UI.** Dumb target (SSH/VM) before rich (K8s).
- **Resolved decisions from vision are constraints**, not choices to revisit: observability-free v1, Git-as-desired-truth, CEL-not-DSL, secrets-never-local, every-interface-authenticated, hard agent guardrails. Encoded in `.roady/spec.yaml` constraints and `AGENTS.md`.

## 2026-06-08 — Stack pinned + plan ordered

- **Module path = `go.klarlabs.de/rollops`** (changed from github.com/klarlabs/rollops) to match org vanity-import convention. Imports updated.
- **Stack components published + pinned** as direct deps (full build resolves):
  `go.klarlabs.de/statekit v1.8.0`, `axi v1.4.0`, `fortify v1.6.0`, `bolt v1.5.2`, `mcp v1.15.0`, `mnemos v0.19.0`; `github.com/felixgeelhaar/decisionkit v0.1.0`.
  Module names are `axi`/`mcp` (not `-go`). decisionkit root has no package — risk gate imports `decisionkit/risk`.
- **`internal/stack/stack.go`** blank-imports all 7 to keep them direct + prove the stack compiles together. Each import migrates into its consuming package as phases land.
- **Roady plan re-authored: flat 63 → 37 ordered tasks** (one per feature, big ones split) with `depends_on` chains across 8 phases. Smart-injection merges rather than replaces, so plan.json was stripped to the `t-*` tasks directly + re-approved. Dependency gating verified: only no-dep roots are `ready`.

## 2026-06-08 — Phase A foundation (config + store)

- **Config package** (`internal/config`): YAML surface, types, embedded JSON Schema (`SchemaJSON`), `Parse` (structural, version/kind gate, `KnownFields` rejects unknowns), `Validate`/`Load` (schema via santhosh-tekuri/jsonschema/v6 + semantic rules). Libs: `gopkg.in/yaml.v3`, `santhosh-tekuri/jsonschema/v6`.
- **CEL in `internal/condition`** (package `condition`, not `cel` — avoids clash with cel-go's `cel` pkg). Typed vars: criticality, environment, changeType, blastRadius, strategy, score. Strict compile (unknown var / syntax / non-bool rejected). `condition.Check` wired into config.Validate for `risk.sensitive` + `rollback.trigger`. Lib: `github.com/google/cel-go`.
- **Store interface CHANGED** (evolved from scaffold): added `SaveObservedState(ctx, TargetState)` + sentinel `store.ErrNotFound`. Reconciler must persist observed state; the scaffolded interface lacked a setter. t-store-iface stays "done" — this is a forward evolution, not a redo.
- **SQLite default backend** (`internal/store/sqlite`): pure-Go `modernc.org/sqlite` (no cgo → single binary). 4 tables (rollouts, target_state, schedules, history). `SaveRollout` upserts rollout + appends history in one tx (crash-safe per transition). WAL + busy_timeout for daemon/CLI coexistence. Times stored RFC3339Nano.
- **Time in tests**: normal Go `time.Now()`/`time.Date` fine here (the Date.now ban is workflow-script-only).
- **Feature branch per task** under `feat/...`; commits are atomic + conventional. Branches unmerged, awaiting review.

## 2026-06-14 — Marketplace, ArgoCD parity, keel→Rollops fleet, v0.16.0

- **Plugin marketplace = curated Git JSON index** (`registry/plugins.json`), no service. sha256 pin = trust anchor; install enforces it + auto-keyless-cosign-verifies when the index names a signer. Lifecycle CLI: search/info/install/list/update.
- **Close ArgoCD/Flux/Rollouts gaps via plugin capabilities, not core bloat.** New caps `trafficrouter` + `metricprovider` mirror the flag-provider pattern. Built: real canary traffic (engine drives SetWeight per step — previously apply-once+bake, weight only drove the flag), OCI + object-storage bucket sources, CRD health via status.conditions, per-target `kubeconfig` multi-cluster. Deliberately NOT: ApplicationSet/Jsonnet/app-of-apps/resource-tree UI.
- **Image automation = semver + digest modes.** Registry reality (marketing sites tag `:latest`+commit-SHA, not semver) forced digest mode (pin `:latest`'s manifest digest, keel "force" parity). The daemon polls the registry (Docker Registry v2 + bearer challenge) and writes bumps back to Git — keel-style auto-deploy, GitOps-native. Rollops image automation is NOT a registry poller in the validate/writeback sense alone; this added the polling loop.
- **Config-in-app-repo** (each app's `.rollops/`), not a central config repo. Operator's call; matches one ArgoCD layout. Safe because their CI builds on release, not every push (no image-bump CI loop).
- **Multi-org git auth = CLASSIC PAT** (`repo` + `read:packages`). Fine-grained PATs are single-owner + selected-repos → can't span two orgs; classic spans all repos the account reaches, no SSO needed (klarlabs has no SAML). Successor (least-privilege, multi-org): per-repo deploy keys / GitHub App — roady #34.
- **rollopsd image override field** on the k8s target: `spec.target.spec.image` overrides the rendered manifest's container image, so image automation's bump reaches the workload.
- **Releases v0.8.0–v0.16.0** this session; cluster pinned to official `rollopsd:v0.16.0`.

## 2026-07-17 — Referenced manifests (`manifestFrom`) + rollback restore, v0.26.0

- **`manifestFrom` references manifests instead of inlining them** (issue #57): the k8s target accepts `spec.target.spec.manifestFrom: { path | kustomize | helm }` as an alternative to inline `spec.target.spec.manifest`. Removes the render-and-paste second-source-of-truth that made Rollops feel heavier than ArgoCD/Flux for the migration it targets.
- **Root = the config file's own directory.** Relative referenced paths resolve against the dir containing the `.rollops` config, not the repo root — matches the operator's intuition. Threaded as `pt.Manifest.Root` (`json:"-"`, never persisted, excluded from checksum): `watch → reconcile → engine → target`; `Plan(ctx,c)` carries it via context (keeps the gRPC `Operations` interface stable), `Apply` via `ApplyRequest.Root`; CLI uses `filepath.Dir(configArg)`.
- **`manifestFrom` is added alongside the existing flat keys, not replacing them.** `spec.helm`/`kustomize`/`manifest`/`oci`/`bucket` keep working (shipped example + tests depend on them); `manifestFrom` is exclusive when present. Non-breaking.
- **Drift for referenced sources keys off the RENDERED output, not the spec text.** `stampReferencedChecksum` re-keys the drift checksum over `Render()` output, so editing a referenced kustomize/helm/path file reconciles even under shallow verification. Inline/flat keep their spec checksum.
- **Keep shelling out to `kubectl kustomize` / `helm template`; do NOT vendor a Go kustomize/Helm SDK.** Those pull `client-go` / `k8s.io/api`, violating the no-Kubernetes-in-core constraint (AGENTS.md, `kubernetes.go` pkg doc). The target already shells out via the injectable `cmdRunner` seam; kept it, at safe defaults (no exec plugins, no `--post-renderer`, `..`/abs path confinement).
- **Rollback restores the captured rendered bytes, not a re-render** (issue #59). Re-rendering the prior spec against the current checkout fails on the manual CLI/UI/MCP/API/gRPC path (no checkout → empty Root) AND is semantically wrong on the daemon if referenced files changed since deploy. Persist `pt.Manifest.Rendered` (JSON `omitempty`, rides the existing desired-manifest blob — no store schema change); `Apply`/`Diff` prefer it; a rollback re-applies exactly what was deployed, root-independent. Inline/flat unchanged (deterministic from Spec).
- **v0.26.0 released; the v0.20-era ghcr image-push blockage is gone** — goreleaser + Docker image both green, `rollopsd:v0.26.0` on ghcr, cluster upgraded. `deploy/kubernetes/rollopsd.yaml` re-synced (was stale at v0.24.0). Release/version comes from the git tag via ldflags (no version file to bump).

## 2026-07-18 — mnemos (org memory) topology

- **mnemos is per-product ISOLATED, not a shared pool** (decided 2026-07-18).
  Org memory must not cross products, so each consuming product owns and pins its
  **own** mnemos deployment in its own `.rollops/` — never a single shared brain
  everyone points at. Current fleet already conforms: `pet-medical` runs its own
  `pet-medical/prod/mnemos`; `senat-os` runtime carries its own mnemos PVC;
  `devatlas` only optionally *clients* mnemos (empty endpoint = disabled in prod);
  the `mnemos` repo's `.rollops/hermes-mnemos.yaml` is **hermes's** instance
  (`hermes/prod/mnemos`), not "the shared mnemos." The apparent digest "drift"
  between `hermes/prod/mnemos` and `pet-medical/prod/mnemos` is expected — they are
  independent instances with independent lifecycles, not one service configured
  twice. Going forward: any new mnemos-using product deploys+pins its own instance;
  do not introduce a shared mnemos endpoint.
