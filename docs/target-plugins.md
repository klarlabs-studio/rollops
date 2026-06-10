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
