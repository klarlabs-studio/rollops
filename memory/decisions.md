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

## 2026-08-11 — The governance gate is wired, and what that changed

The seam existed and nothing called it: `Hook.Evaluate` had no caller and no
provider, so a documented governance feature was doing nothing. Now `Apply` calls it.

- **The gate refuses; it does not escalate.** Steps 1 and 2 of `Apply` (confinement
  floor, risk gate) feed `needApproval`. This one does not: `Allowed == false` blocks.
  The point of delegating a decision is that the answer is binding, and escalating to
  approval would let an approver here overrule the system that was asked precisely
  because it knows something the engine does not.
- **Both entrypoints, not just the daemon.** `cmd/rollops` builds an in-process engine
  for one-shot use. Wiring the gate only into `rollopsd` would leave `rollops apply`
  on a laptop as the way around it, and a gate you can walk around is not one.
- **Configured from the environment, like notify.** `ROLLOPS_GOVERNANCE_URL` /
  `_SECRET` / `_TIMEOUT`. No config-file block, so a signing secret never has to live
  in a committed file. An unparseable timeout keeps the 5s default rather than failing
  startup — refusing to boot over a mistyped duration takes the deploy path down for a
  formatting error.
- **Anything that is not a parseable 200 denies.** A 500, an HTML error page from a
  proxy, a timeout: none of these are decisions. Treating a broken governor as
  permission is the same failure as treating an unreachable one as permission. The
  audit entry distinguishes the two, because reporting an outage as a policy refusal
  sends someone to read a policy that never ran.
- **`doctor` reports reachability.** A fail-closed dependency on the deploy path
  should not be discovered during an incident. The probe sends `action: "probe"` so a
  governor can tell a readiness check from a deploy decision and not record it.
- **`Request` gained `Environment` and `Version`.** A target ref alone is not enough
  to decide on: prod and staging deserve different answers, and a governor holding a
  release record needs the version to find it. Version comes from the
  `rollops.version` label — a declared fact, not one guessed out of a target-specific
  spec whose shape differs per kind.
- **The wire structs are mapped field by field, not converted,** though they line up
  today. `wireDecision` is a contract with software outside this repo; `Decision` is
  ours. A conversion would tie them together and invite someone to "fix" a compile
  error by editing the wire type, silently changing what every governor must send.
  (Carries a `//nolint:staticcheck` saying so.)
- **Still no dependency.** `TestNoGovernorDependency` passes; nothing in `internal/`
  or `cmd/` names relicta. The provider is generic HTTP — a script answering the
  contract works as well as a product.

## 2026-08-12 — Coverage is gated, and why the floors look the way they do

The shared CI bar was adopted with `coverage: false` (#24). Its own input description
explains what that meant: "set false for repos without a threshold yet". There was no
`.coverctl.yaml`, so there was nothing to check. Now there is.

- **The floors are the current state, not an ambition.** A gate that fails the day it
  arrives gets switched off, and a gate that is off protects nothing. Each domain sits
  about two to three points under measured coverage — enough margin that ordinary churn
  does not trip it, tight enough that a real regression does. Raising one is then a
  deliberate edit somebody reviews.
- **The deploy path carries the highest floors.** governance 90, engine 80, security 88,
  risk 87. These decide whether a rollout proceeds and whether it can be undone; the rest
  of the tree is gated lower because that is where it honestly is.
- **cmd and metricplugin are 0.** Both are wiring covered indirectly through what they
  compose, so a floor would measure how far a test happens to walk into main() rather
  than anything about the code.
- **Nothing is left unmatched.** coverctl gates the domains it is given, so a package in
  no domain has no floor — which reads as coverage nobody has. An `internal` catch-all
  domain covers the remainder rather than leaving gaps. (Relicta had exactly this hole:
  nine domains' worth of code, including its whole REST API, sat outside every domain
  while the gate reported all-pass.)
- **Verified against CI's actual commands**, not a local approximation: the shared bar
  runs `go test -race -covermode=atomic -coverprofile=coverage.out ./...` then
  `coverctl check --profile=coverage.out`, which attributes coverage per-package rather
  than across a `-coverpkg` set. Both paths produce identical figures here, but the
  difference is real and worth checking before trusting a threshold.
- Generated protobuf stubs are excluded. They have no hand-written statements and only
  move the number around.

## 2026-08-12 — All three plugin builders now honour their caller's context

#113 fixed featureflags.BuildProvider and stopped there. There were three builders with
the same defect — trafficrouting.BuildRouter and metricplugin.Build both hardcoded
context.Background() for the subprocess launch and the manifest RPC, while the engine call
sites that invoke them (driveTraffic, runAnalysis) had a ctx in scope and dropped it. So a
cancelled rollout still waited out each plugin's own timeouts on a subprocess nobody was
waiting for.

Found by looking at why trafficrouting sat at 42% once coverage was gated. The low number
was not itself the problem; it was the signal pointing at code nobody had exercised.

- **Asserted at the engine, not in each builder.** The engine is where the context
  originates, and the builder seams can observe exactly what they are handed. A test
  inside trafficrouting could not do it: sha256 verification runs before the launch, so a
  cancelled context never reaches the code that would honour it, and such a test would
  pass without exercising anything. One was written that way first and deleted.
- **baseArgs was untested**, and it decides which cluster kubectl talks to. Wrong there
  does not fail loudly — it shifts production traffic on whatever cluster the ambient
  kubeconfig names. Now covered, including that the flags reach kubectl rather than merely
  being computed: a correct baseArgs that nothing passes targets the ambient cluster while
  looking configured.
- The trafficrouting floor is ratcheted 40 → 47 to hold the gain (coverage 42.3% → 50.0%).

## 2026-08-12 — SSH command quoting was not quoting, and a malformed host-key pin failed open

Both found by asking why internal/target/ssh sat at 26% once coverage was gated. The
number was the signal, not the problem.

- **shellQuote wrapped values in single quotes without escaping the ones inside**, so a
  value containing an apostrophe closed the quoting and the rest was interpreted by the
  remote shell. Its comment justified this: "paths are operator-controlled config, not
  end-user input". That premise contradicts our own threat model — internal/security/
  confine.go says "In the documented 'one repo per customer' model the repo config is
  untrusted", and a target spec comes from that config. The confinement allowlists exist
  because a poisoned repo is an expected input, and a path is as good a place to hide a
  command as a command is. It was also a plain correctness bug for a trusted operator:
  /home/o'brien/app is a legitimate path that produced a broken command.
- **A pinned host key that failed to parse fell through.** Combined with a stale
  insecureSkipHostKeyCheck from dev, that removed verification altogether — the one
  outcome neither setting expresses. An operator who sets hostKey has asked for
  verification, so an unparseable pin is now refused, naming the parse error so the typo
  is findable rather than reported as "no pinned host key" when one is plainly set.
- **The quoting tests run values through a real shell** and detect injection by whether a
  file was created, not by searching output for a marker. The first version searched
  stdout for "INJECTED" — which the hostile path contains verbatim — so it could not tell
  an intact string from an executed command, and failed against the working fix.
- The rest of hostKeyCallback was already right and is now pinned by tests: a pin
  verifies, a mismatched key is refused, the explicit opt-in works, and an unpinned host
  is refused rather than trusted on first use.

## 2026-08-12 — The image verified nothing it downloaded

The Dockerfile fetched kubectl over curl and ran it. That binary is what drives the
user's cluster — it applies manifests, patches HTTPRoutes and shifts production traffic —
so the image's integrity rested on the CDN object still being what it was when the tag was
written.

Rollops already holds plugin binaries to exactly this standard: pluginhost.VerifyArtifact
refuses a plugin whose sha256 does not match its pin. The image was the one place shipping
an unpinned executable, and it was the one binary with the most authority.

- **Pinned, not fetched from the adjacent .sha256.** A checksum retrieved from the same
  host as the artifact is not an independent check: a host able to serve a substituted
  binary can serve a matching digest alongside it. Pinning moves the trust decision into a
  reviewed commit.
- **Verified in both directions**, because a check that cannot fail is decoration: the
  build prints "kubectl: OK", and building with a zeroed digest exits 1 with "1 of 1
  computed checksums did NOT match".
- **The checksum is written to a file and checked, not piped into sha256sum.** The first
  version piped it, which put a pipe on a RUN line that also mentions curl — matching the
  "remote script piped directly to shell" rule (nox IAC-023, CWE-94) and failing CI's
  net-new-high gate. The verification is identical either way, and that rule is worth
  keeping sharp for the case where it is right; waiving a "piped to shell" high finding in
  a Dockerfile is how a baseline stops being trustworthy.
- Found by reading, not by the scanner. nox's Dockerfile findings were the other two rules
  and both were false positives — see below.

### The Dockerfile findings nox did report were wrong

IAC-123 (COPY without --chown) fires three times. Two are in the build stage, which is
discarded. The third copies the daemon binary into the final image, where it is owned by
root and mode 0755 while the process runs as uid 10001 — so the process cannot modify its
own executable. Adding --chown would hand it that ability. Following this finding would be
a regression, which is worth recording before someone "fixes" it.

CONT-001 (base image not digest-pinned) is a real practice with a real cost: a pinned
digest stops receiving base-image security patches until someone bumps it by hand, so it
trades one supply-chain risk for another and wants automation (Renovate) alongside it
rather than a bare pin. Left open deliberately.

## 2026-08-12 — Container hardening on rollopsd, minus the one line that needs a cluster

The pod already ran as uid 10001 with a non-root securityContext. Added the privileges a
non-root process can still acquire or retain: allowPrivilegeEscalation: false (a setuid
binary is otherwise still a path up), capabilities drop ALL (the daemon binds :8443 and
shells out to kubectl; neither needs one), and seccompProfile RuntimeDefault at pod level —
unset means Unconfined on most runtimes, leaving the whole syscall surface reachable from a
process that needs an ordinary subset.

- **readOnlyRootFilesystem is deliberately absent.** It is the natural fourth line. rollopsd
  writes its database to a mounted volume, but whether it ever writes elsewhere — a temp
  file, a plugin unpacked to disk, a kubectl cache — could not be established by reading,
  and getting it wrong stops the daemon at startup in someone's cluster. It wants an
  in-cluster run, so it is a separate change rather than a guess bundled into a safe one.
- Validated with `kubectl apply --dry-run=client` (schema only, no cluster mutation) and by
  rescanning: IAC-145 is gone, total findings 93 → 92, and the only high remains the
  already-baselined TAINT-004.

### The remaining rollopsd.yaml findings are mostly category errors

IAC-132 wants a PodDisruptionBudget and IAC-142 pod anti-affinity. This is a single-replica
Deployment with strategy Recreate, because it is the single writer to a SQLite PVC. A PDB
over one replica either blocks node drains outright or does nothing, and anti-affinity
across one pod is meaningless. IAC-131/286 want a NetworkPolicy, which is a property of the
cluster the operator runs, not of a manifest we ship.

Recorded because these will be flagged again on every scan, and the answer is "not
applicable to a single-writer daemon", not "not done yet".
