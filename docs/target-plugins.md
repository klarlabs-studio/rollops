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

The current plugin protocol is `internal/target/plugin.ProtocolVersion == 1`.
Plugins must complete the handshake with:

- `ProtocolVersion: 1`
- `Cookie: ROLLOPS_TARGET_PLUGIN_V1`

A version or cookie mismatch is rejected before rollout operations can run.

The RPC wire carries:

- `Apply(kind, spec, checksum) -> changed, detail`
- `Observe() -> fingerprint value, metadata`
- `Health() -> state, reason`

`state` must map to one of the concrete health states:

- `HealthHealthy`
- `HealthDegraded`
- `HealthUnhealthy`

`HealthUnknown` and unknown integer values are rejected by the adapter.

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

A plugin is a standalone Go binary: implement `pkg/target.Target` and hand it
to `pkg/plugin.Serve`:

```go
package main

import (
	"go.klarlabs.de/rollops/pkg/plugin"
	pt "go.klarlabs.de/rollops/pkg/target"
)

func main() {
	var t pt.Target = newMyTarget() // your implementation
	if err := plugin.Serve(t); err != nil {
		panic(err)
	}
}
```

`Serve` listens on a private unix socket, prints one handshake line on stdout
(`ROLLOPS_PLUGIN|<version>|<cookie>|<socket>`), and serves gRPC until stdin
closes — the host's shutdown signal. Log to stderr; stdout belongs to the
handshake.

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
