# Target Plugins

Target plugins let Rollops support infrastructure that should not live in core.
A plugin-backed target must behave exactly like a first-party target: idempotent
apply, stable observe fingerprint, concrete health, and no secret leakage.

## Contract

The public Go contract is `pkg/target.Target`:

```go
type Target interface {
	Apply(context.Context, Manifest) (Result, error)
	Observe(context.Context) (Fingerprint, error)
	Health(context.Context) (HealthStatus, error)
}
```

Every implementation must pass `pkg/conformance.Run`.

## Protocol

Rollops plugins speak one generic gRPC service (`rollops.plugin.v1.Plugin`),
modeled on nox-hq's architecture. A plugin declares a **manifest** —
capabilities grouping named **tools**, plus the **safety scopes** it needs —
and the host invokes tools generically. New plugin kinds are new capabilities,
not new services.

Lifecycle:

1. The host launches the plugin binary (sha256-pinned).
2. The plugin prints one handshake line on stdout and serves gRPC on a private
   unix socket. A protocol-version or cookie mismatch is rejected.
3. The host calls `GetManifest` and validates the declared safety requirements
   against its policy — a plugin that demands more than the policy allows is
   refused before any tool runs.
4. The host calls `InvokeTool(capability, tool, json)` per operation.

A **target plugin** declares the `target` capability with three tools:

- `apply(kind, spec, checksum) -> changed, detail`
- `observe() -> value, meta`
- `health() -> state, reason`

`state` must map to a concrete health state (`HealthHealthy`, `HealthDegraded`,
`HealthUnhealthy`); `HealthUnknown` and unknown values are rejected.

## Required Semantics

- `Apply` must be idempotent. Reapplying the same manifest must return
  `Changed=false`.
- `Observe` must return a stable fingerprint for identical live state.
- If a manifest checksum is supplied, `Observe` after `Apply` should report that
  checksum unless the target has a stronger normalized fingerprint contract.
- `Health` must return a concrete state and a useful reason when degraded or
  unhealthy.
- Plugin metadata, detail strings, health reasons, and errors must not contain
  secret material.
- Rollback is driven by Rollops applying a prior manifest; plugins do not need a
  separate rollback method.

## Conformance Test

Plugin authors should include a test like:

```go
func TestTargetConformance(t *testing.T) {
	conformance.Run(t, func() (target.Target, error) {
		return NewTargetForTest(), nil
	}, target.Manifest{
		Kind:     "example",
		Spec:     []byte(`{"version":1}`),
		Checksum: "example-v1",
	})
}
```

Run it before publishing the plugin and again whenever Rollops bumps the plugin
protocol version.

## Authoring a Plugin

A target plugin is a standalone Go binary: implement `pkg/target.Target` and
hand it to `pkg/plugin.ServeTarget`, which builds the `target`-capability
manifest and wires the three tools for you:

```go
package main

import (
	"go.klarlabs.de/rollops/pkg/plugin"
	pt "go.klarlabs.de/rollops/pkg/target"
)

func main() {
	var t pt.Target = newMyTarget() // your implementation
	safety := plugin.Safety{
		NetworkHosts: []string{"api.acme.com:443"}, // declare what you reach
		RiskClass:    plugin.RiskActive,
	}
	if err := plugin.ServeTarget("acme/exotic", "1.0.0", t, safety); err != nil {
		panic(err)
	}
}
```

`ServeTarget` listens on a private unix socket, prints one handshake line on
stdout (`ROLLOPS_PLUGIN|<version>|<cookie>|<socket>`), and serves the generic
Plugin service until stdin closes — the host's shutdown signal. Log to stderr;
stdout belongs to the handshake.

Declare the **safety** scopes your plugin actually needs (network hosts, file
paths, env vars, risk class). The host validates them against its policy before
invoking any tool — by default network egress must be allow-listed and only up
to `active` risk is admitted. For a custom capability, drop to `NewManifest` /
`NewServer` / `Serve` directly.

## The Marketplace

The plugin marketplace is a curated, version-controlled index
(`registry/plugins.json` in the rollops repo) that maps a plugin name to its
published releases: per-platform download URLs, each with a sha256 pin, plus
optional cosign identity. It is just a JSON file served over HTTPS — no service
to run — so the index itself is reviewed in Git like any other change.

Search it and install by name:

```sh
rollops plugin search                 # list every published plugin
rollops plugin search flag            # match name, description, or capability

rollops plugin info flagsmith         # every version, artifact, pin, cosign id

rollops plugin install flagsmith                 # latest, current OS/arch
rollops plugin install flagsmith --version v0.1.0 # pin a version

rollops plugin list                   # installed plugins + their sha256 pins

rollops plugin update                 # report which installed plugins are outdated
rollops plugin update --apply         # upgrade outdated plugins to their latest
rollops plugin update flagsmith --apply  # upgrade just one
```

`plugin update` identifies each installed binary by matching its sha256 against
the registry's published artifacts — so a plugin renamed with `--name` at
install time is still recognised — and compares its version to `latest`. It is
**dry-run by default**: it reports `up to date`, `outdated (name old -> new)`,
or `unknown (not from this registry)` per binary and changes nothing. Pass
`--apply` to actually upgrade outdated plugins in place; each upgrade runs the
same sha256-pin and cosign checks as a fresh install. After upgrading, the new
sha256 is printed — update the pin in your rollout specs to match.

`plugin info <name>` prints the full registry detail for one plugin — each
published version with its per-platform artifacts and sha256 pins, plus the
cosign identity when the release is signed — so you can inspect before
installing. `plugin list` is offline: it reads the install directory and
re-derives the sha256 each binary pins to (the same value `install` printed),
so you can recover a pin for a spec without re-downloading.

Resolving a name picks the artifact for the running OS/arch, downloads it, and
**enforces the index's sha256 pin** — a download whose hash differs from the
published pin is rejected, never installed. The default install name is the
plugin name; the printed sha256 is what you then pin in your spec.

If the index declares a cosign identity for the release **and** the artifact
carries signature material (a sigstore `bundle`, or a detached `signature` +
`certificate`), the install **verifies the cosign signature automatically** —
keyless, with the index's identity and issuer as the expected signer — before
the binary is placed. No flags required; a failed verification blocks the
install. Manual `--cosign-*` flags still take precedence when given. So a signed
marketplace install enforces two independent checks: the curated sha256 pin and
the publisher's signature.

Override the index with `--registry <url>` or `ROLLOPS_PLUGIN_REGISTRY` (e.g. a
private registry). A source containing `://` or matching a local file is treated
as a direct path/URL install instead of a marketplace name (see below).

## Installing a Plugin

`rollops plugin install` also accepts a plugin binary directly (a local path or
`https://` URL), optionally verifies its cosign signature, installs it into the
plugin directory, and prints the sha256 to pin:

```sh
# from a release URL, verifying a keyless cosign signature
rollops plugin install https://example.com/rollops-plugin-flagsmith \
  --signature flagsmith.sig --certificate flagsmith.pem \
  --cosign-identity 'https://github.com/acme/.github/...' \
  --cosign-issuer 'https://token.actions.githubusercontent.com'

# or key-based, or just install + hash a local binary
rollops plugin install ./rollops-plugin-flagsmith --cosign-key cosign.pub
rollops plugin install ./rollops-plugin-flagsmith
```

Default install dir is `~/.rollops/plugins` (override with `--dir` or
`ROLLOPS_PLUGIN_DIR`). The trust chain: the signature is verified once at
install; the printed sha256 is then pinned in the spec and enforced on every
launch, so the bytes that ran through cosign are the bytes that execute.

## Using a Plugin

Declare the target with the `plugin` kind. The binary's sha256 pin is
required — Rollops refuses to execute an unpinned or tampered binary:

```yaml
target:
  kind: plugin
  ref: acme/prod/exotic
  spec:
    binary: /usr/local/lib/rollops/plugins/exotic
    sha256: 4f5a…              # shasum -a 256 <binary>
    # everything else is your plugin's own configuration; it arrives
    # verbatim in the Apply manifest spec
    region: eu-central
```

Per engine operation the host verifies the pin, launches the binary, completes
the handshake, drives Apply/Observe/Health over gRPC on the unix socket, and
tears the process down afterwards. A version or cookie mismatch, a missing
handshake, or a hash mismatch fails the operation before anything runs.

## Packaging

Ship a plugin as a plain binary plus its sha256. Recommended layout:

- install to `/usr/local/lib/rollops/plugins/<name>`
- publish `checksums.txt` next to your release artifacts
- operators copy the digest into the target spec's `sha256` field

Rebuilding the plugin changes the hash; update the pin in Git alongside the
binary rollout so the change is reviewed like any other desired-state change.

## Security model

A plugin is a subprocess that the daemon launches; treat it as you would any
binary you run as the daemon's user.

- **Binary integrity.** The `sha256` pin is verified before exec, and the
  path is symlink-resolved so the verified file is the executed file. One
  residual race remains: an attacker who can overwrite the binary's inode
  between the hash check and exec defeats the pin. Install plugins in a
  directory writable only by root (or the daemon's deploy user) — e.g.
  `/usr/local/lib/rollops/plugins/` on a trusted mount — never a
  world-writable or operator-writable location.
- **Secrets.** Plugins do **not** receive resolved secrets. `secret:<ref>`
  values in a plugin target's spec reach the plugin as the literal reference
  string, not the plaintext (secret resolution is reserved for first-party
  targets). A plugin that needs a credential should read it from its own
  environment or a path the operator controls, not from the target spec.
- **Trust.** A plugin runs with the daemon's privileges and can do anything
  that user can. Only run plugins you have reviewed or trust the publisher of,
  exactly as for the daemon binary itself.
