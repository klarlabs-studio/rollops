
## Target plugin process lifecycle + distribution

Run third-party target plugins as real subprocesses: launch the plugin binary, verify the handshake (protocol version + cookie) over a simple stdout line, dial its gRPC server on a unix socket, adapt it into pkg/target.Target, and tear the process down cleanly after the operation. Verify plugin binary integrity (sha256 pin in the target spec) before exec. Wire a "plugin" target kind into the registry/config schema so a plugin-backed target is declared in rollops.yaml like any other target. Document authoring + packaging.

---

## Manifest-capability plugin architecture (nox-style) + feature-flag plugins

Replace the typed TargetPlugin gRPC service with a single generic Plugin service (GetManifest + InvokeTool), modeled on nox-hq's plugin architecture. Plugins declare capabilities (named groups of tools) and safety requirements (network hosts, file paths, env vars, risk class, needs-confirmation) via a manifest the host validates against a safety policy before invoking — capability-scoped trust instead of full daemon trust. Provide an ergonomic pkg/plugin SDK (manifest builder + HandleTool + Serve; typed convenience wrappers ServeTarget and ServeFlagProvider). Migrate the existing target-plugin path onto a 'target' capability (apply/observe/health as tools). Add feature-flag providers as the first new capability: a flag plugin declares 'featureflag' with an apply_flag tool; wire featureflags.Hook into the engine to fire per progressive step (percentage tracks traffic weight) and/or on promote, configurable via a featureFlags spec block. sha256-pin distribution as today; cosign signing is a later follow-up. Breaking vs v0.5.0 plugin protocol (pre-1.0, no external plugins yet). Document authoring, capabilities, safety policy, and the flag integration.

---

## Per-repo least-privilege git auth (deploy keys / GitHub App) for the reconcile watch

Replace the single broad classic PAT (currently used for ROLLOPS_WATCH across klarlabs-studio + felixgeelhaar repos) with least-privilege, per-repo credentials. Two viable designs: (a) per-repo SSH deploy keys — the watch already supports `deployKeyPath`, so wire one key per repo (mount a Secret per repo, switch URLs to git+ssh, read+write scoped to that single repo); naturally multi-org. (b) GitHub App installation tokens — install the App on the org + personal account, mint short-lived per-installation tokens (least privilege, auto-rotating, multi-owner); the git layer's Auth would gain an App-token provider and the watch config a per-repo/app reference. Motivation: a fine-grained PAT is scoped to ONE owner (can't span klarlabs-studio + felixgeelhaar), and a classic PAT is broad (all the operator's repos) — both poor for a daemon that writes image-automation bumps back to many repos across orgs. Acceptance: each watched repo authenticates with a credential scoped to just that repo; no single token grants access beyond what a repo needs; tokens never written to disk or logs (redaction already shipped); rotation is per-repo. Surfaced while dogfooding the keel→Rollops fleet cutover (a broad PAT leaked into pod logs on a clone crash; fine-grained PAT 403'd on cross-org repos).

---
