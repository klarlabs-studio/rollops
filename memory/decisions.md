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

- **`go.sum` stays excluded from nox scanning; the fix belongs in nox** (decided
  2026-07-19). A note in open-threads claimed excluding `go.sum` "drops dependency
  enumeration to 0" and implied 28 repos needed fixing. Measured across all 28:
  false. Enumeration continues from `go.mod`, and `go.sum` findings are ~99%
  false-positive because it hashes the entire module graph, not the build — 148 of
  148 Go findings on mnemos named versions MVS never selected. The exclusions were
  correct workarounds. Removing them would have injected ~5,263 false positives
  fleet-wide. **Rule going forward: verify a dependency finding against
  `go list -deps` (packages actually imported) before calling it real — not
  `go list -m all`, which reports the module graph and over-reports.**

- **nox is the single dependency scanner; govulncheck is ruled out** (operator
  decision, 2026-07-19). When the `go.sum` blind spot surfaced, the obvious fix was
  restoring govulncheck (version- and reachability-aware) in the shared workflow.
  Operator declined: "we use nox and don't want govulncheck." That scoped the work
  to making nox correct rather than adding a second scanner, and produced
  Nox-HQ/nox#248.

- **Unreachable dependency findings are demoted, never dropped** (decided
  2026-07-19, implemented in Nox-HQ/nox#248). Reachability rests on a `go list`
  call that can be wrong, and a silently vanished finding is indistinguishable from
  a scanner that missed it. Unreachable → `info`, with `reachable=false` and
  `affected_imports` recorded and the reason in the message. Conclusions are drawn
  only from positive evidence: an advisory with no import metadata, or a build that
  cannot be enumerated, leaves the finding exactly as it was.

- **The shared `go-ci.yml` pin was bumped to nox 1.10.0 knowing agent-go breaks**
  (decided 2026-07-19). A canary measured the blast radius first: agent-go 56
  gate-failing findings (42 critical), senat-os 3, mnemos 2, seven other repos 0.
  Merged anyway — those vulnerabilities were always present and passing as
  `medium`; a red build is a fair representation of the actual state, and a stale
  pin is precisely how they stayed invisible. Consequence to expect: agent-go CI
  red until its criticals are triaged.

- **RETRACTION: rollops does NOT have a live `GO-2026-5932`** (2026-07-19). An
  earlier entry in open-threads recorded it as a confirmed true positive in
  `x/crypto@v0.53.0`. It is a false positive: the advisory has `introduced: 0`, no
  fixed version, and is scoped by OSV to `x/crypto/openpgp` — which rollops does not
  import (it links `chacha20`/`cryptobyte`). The original claim came from checking
  module@version against the build and stopping there. → supersedes the
  "rollops has a live dependency vuln" note of 2026-07-19.

## 2026-08-11 — External governance and deployment evidence

- **Governance and deployment evidence cross a wire protocol, never a Go
  dependency.** Rollops must not depend on relicta and relicta must not depend on
  Rollops: they are separate products, and a user of one must never be obliged to
  adopt the other. That rules out a relicta-shaped `governance.Provider` in this
  tree, and a Rollops-shaped receiver in relicta's. Recorded in full as relicta
  ADR-012 and summarized in `docs/external-governance.md`; the two records are
  deliberately independent so neither repo has to read the other's docs.
- **Both flows are Rollops-initiated.** Rollops asks "may I deploy version V to
  environment E?" before rolling out, and reports "V reached E at T" after. The
  governor never reaches into a cluster, holds no cluster credentials, and does not
  poll. This also settles fidelity: a manifest commit is a *request* to deploy,
  while the controller reporting healthy is the *fact*, and only we know the fact.
- **The provider stays generic.** `internal/governance.Provider` gets an HTTP
  implementation configured with a URL, a signing secret and a timeout — not a
  named integration. Anything answering the documented contract works. With no URL
  configured, behaviour is exactly as today (`Hook` returns allowed), so nothing
  changes for a user who has not asked for governance.
- **A configured gate fails closed.** `Hook` returning allowed with no provider is
  correct — governance not requested must not block. But once a provider *is*
  configured and unreachable, the answer is deny. A gate that evaporates when the
  network is bad is absent exactly when a rushed deploy is most likely. "Not
  requested" and "requested but unavailable" are different states and must not
  produce the same outcome.
- **`notify.Event` gains `version` and `environment`.** The payload carries
  `{kind, target_ref, rollout_id, detail}` today, and a deployment record needs the
  version that landed. A receiver should not have to call back to understand what it
  was told, so the fields go in the event rather than being resolved afterwards.
- **Two risk scores, two questions, neither recomputed.** We score the *rollout* —
  which target, what traffic share, what depends on it, what a failure there costs.
  An external governor scores the *change* — what is in it. When we ask for a
  decision, its score arrives in `Decision.Evidence` as a fact we record, not as an
  input we re-derive. Written down because two components producing one number for
  one change is a known source of silent disagreement.
- **Invariant:** this repo must build, test and pass CI with relicta absent, and
  `go.mod` must never reference it. Worth a test asserting the absence, because the
  decision decays the first time someone reaches for a convenient import.
