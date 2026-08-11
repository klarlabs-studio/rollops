
## Multi-cluster at scale (RolloutSet generators)

Deploy a service across N clusters without per-cluster boilerplate, ArgoCD
ApplicationSet-style. Scoped in `docs/design/multi-cluster-scale.md` (draft RFC):
recommended approach is a `RolloutSet` kind + cluster registry that expands
in-memory to N ordinary configs (no engine/store change). Phased: list generator
→ cluster generator → matrix/git + fleet status rollup. Not yet approved.

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

- **The 10s plugin handshake budget is load-dependent.**
  `pluginhost.TestLaunch_ConfinesEnvironment` and
  `featureflags.TestBuildProvider_EndToEnd` build a helper binary and wait 10s for
  the gRPC handshake. Both fail intermittently when `go test ./...` runs on a busy
  machine — compilation competes with the handshake and the budget expires — and
  pass in isolation, warm, or cold. Observed twice on 2026-08-11. This is a timeout
  measuring the machine rather than the code, so it will surface as an unexplained
  red CI run. Either raise the budget substantially for the test path or pre-build
  the helper binary before starting the clock.

---
