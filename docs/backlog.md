
## Make it real (near roadmap)

Canonical: `docs/design/make-it-real.md`. Roady features `real-trust`,
`real-agent`, `real-canary`, `real-gitops`.

Phase 1 of this RFC (list generator only) is **in** that program. Cluster
generator and fleet status rollup shipped after; matrix/git stay later work.

---

## Multi-cluster at scale (RolloutSet generators)

Deploy a service across N clusters without per-cluster boilerplate, ArgoCD
ApplicationSet-style. Scoped in `docs/design/multi-cluster-scale.md`.
**List + cluster generators and fleet status rollup are in.** Matrix/git
generators and promotion waves remain later work.

---

<!-- DONE: Target plugin process lifecycle + distribution — shipped. Subprocess
     launch + handshake (pkg/plugin/handshake.go), unix-socket gRPC dial + clean
     teardown (internal/pluginhost/launch.go), sha256 pin (VerifyBinary), and a
     'plugin' target kind in the registry (internal/target/builtin.go). See
     docs/target-plugins.md. -->

<!-- DONE: Manifest-capability plugin architecture (nox-style) + feature-flag
     plugins — shipped. Generic Plugin service GetManifest+InvokeTool
     (proto/rollops/plugin/v1/plugin.proto); pkg/plugin SDK (manifest builder,
     Serve/HandleTool, ServeTarget/ServeFlagProvider/ServeTrafficRouter/
     ServeMetricProvider); capabilities target/featureflag/trafficrouter/
     metricprovider; host safety-policy validation (internal/pluginhost/policy.go).
     needs_confirmation is now a load-time gate: a plugin declaring it won't load
     unless named in ROLLOPS_PLUGIN_CONFIRM (or "*"). -->

---

## Plugin supply-chain & policy — later iteration

Net-new hardening on top of what shipped (sha256 pin + cosign key-based
signature verification + capability/risk/confirmation policy). Not gaps; deferred
by choice.

- **Keyless / Rekor signature verification.** Today only cosign *key-based* blob
  signatures are verified (stdlib ECDSA/Ed25519/RSA, no sigstore dep). Add
  keyless (Fulcio cert + Rekor transparency-log) verification so plugins signed
  by an OIDC identity are trusted without a pre-shared public key. Cost: pulls in
  the sigstore dependency tree — weigh against the single-static-binary story
  (maybe behind a build tag or an optional verifier).

- **Multi-key / per-publisher signing.** `ROLLOPS_PLUGIN_PUBLIC_KEY` is a single
  trusted key for the whole fleet. Support a set of trusted keys (e.g. a keyring
  dir, or per-plugin key in the marketplace registry) so plugins from different
  publishers each verify against their own key.

- **Per-tool invoke-time gating.** Per-tool risk is admission introspection only
  (effective-risk at load time). A real invoke-time gate — block/confirm an
  individual `invasive` or `needs_confirmation` tool call while letting passive
  tools through — needs a confirmation flow that suits a headless daemon (e.g.
  reuse the rollout approval mechanism). Deliberately not built: interactive
  per-invoke confirmation doesn't fit the reconcile loop without that design.

<!-- DONE: Per-repo least-privilege git auth (deploy keys / GitHub App) — shipped.
     git.Auth gained a TokenSource provider seam; git.GitHubApp mints short-lived,
     auto-rotating installation tokens (JWT → installation access token, cached +
     refreshed). watch.json gains githubAppId/githubInstallationId/
     githubAppPrivateKeyFile per repo; deploy keys + PAT still supported. See
     docs/git-auth.md. -->

<!-- DONE: The load-dependent plugin handshake — fixed, and it was a real bug not just
     a flaky test. awaitHandshake bounded its wait with a bare 10s timer and ignored the
     context, so a cancelled caller still waited the full ten seconds on a plugin that
     would never answer. It now honors the caller's deadline (shorter or longer) and
     returns on cancellation with an error that reads as cancellation rather than a
     timeout. BuildProvider gained a ctx for the same reason: the engine's two call sites
     had one in scope and were dropping it. -->

---

## Fleet status rollup

Multi-cluster RFC Phase 3a (lean). Aggregate latest-per-target rollout phases for a RolloutSet-style prefix (web@ → web: 9/10 promoted). Engine FleetRollup + gRPC/CLI/HTTP. No store schema change. Matrix/git generators and promotion waves remain out of scope (TDD forbids ApplicationSet matrix).

---
