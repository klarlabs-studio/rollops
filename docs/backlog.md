
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

<!-- DONE: Per-repo least-privilege git auth (deploy keys / GitHub App) — shipped.
     git.Auth gained a TokenSource provider seam; git.GitHubApp mints short-lived,
     auto-rotating installation tokens (JWT → installation access token, cached +
     refreshed). watch.json gains githubAppId/githubInstallationId/
     githubAppPrivateKeyFile per repo; deploy keys + PAT still supported. See
     docs/git-auth.md. -->

---
