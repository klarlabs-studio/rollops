
## Target plugin process lifecycle + distribution

Run third-party target plugins as real subprocesses: launch the plugin binary, verify the handshake (protocol version + cookie) over a simple stdout line, dial its gRPC server on a unix socket, adapt it into pkg/target.Target, and tear the process down cleanly after the operation. Verify plugin binary integrity (sha256 pin in the target spec) before exec. Wire a "plugin" target kind into the registry/config schema so a plugin-backed target is declared in rollops.yaml like any other target. Document authoring + packaging.

---

## Manifest-capability plugin architecture (nox-style) + feature-flag plugins

Replace the typed TargetPlugin gRPC service with a single generic Plugin service (GetManifest + InvokeTool), modeled on nox-hq's plugin architecture. Plugins declare capabilities (named groups of tools) and safety requirements (network hosts, file paths, env vars, risk class, needs-confirmation) via a manifest the host validates against a safety policy before invoking — capability-scoped trust instead of full daemon trust. Provide an ergonomic pkg/plugin SDK (manifest builder + HandleTool + Serve; typed convenience wrappers ServeTarget and ServeFlagProvider). Migrate the existing target-plugin path onto a 'target' capability (apply/observe/health as tools). Add feature-flag providers as the first new capability: a flag plugin declares 'featureflag' with an apply_flag tool; wire featureflags.Hook into the engine to fire per progressive step (percentage tracks traffic weight) and/or on promote, configurable via a featureFlags spec block. sha256-pin distribution as today; cosign signing is a later follow-up. Breaking vs v0.5.0 plugin protocol (pre-1.0, no external plugins yet). Document authoring, capabilities, safety policy, and the flag integration.

---

<!-- DONE: Per-repo least-privilege git auth (deploy keys / GitHub App) — shipped.
     git.Auth gained a TokenSource provider seam; git.GitHubApp mints short-lived,
     auto-rotating installation tokens (JWT → installation access token, cached +
     refreshed). watch.json gains githubAppId/githubInstallationId/
     githubAppPrivateKeyFile per repo; deploy keys + PAT still supported. See
     docs/git-auth.md. -->

---
