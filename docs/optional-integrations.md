# Optional Integrations

Optional integrations are provider seams. Rollops defines the contract and
validation, but core does not require SaaS vendors or external control planes.

## Feature Flags

`internal/featureflags.Hook` accepts an injected provider and validates a flag
change before calling it:

- flag name is required
- rollout percentage must be `0..100`
- nil provider is inert

This is now a shipped feature, not just a seam: a feature-flag provider is a
gRPC plugin and the rollout drives it per progressive step and/or on promote.
See `docs/feature-flags.md`.

## Governance

`internal/governance.Hook` accepts an injected provider and validates a
governance request before calling it:

- action is required
- target ref is required
- nil provider is inert and allows the action
- denial always carries a reason
- evidence is returned as structured key/value data

The hook can approve, reject, or annotate rollout actions without replacing the
hard Rollops policy floor, RBAC checks, or audit attribution.

This is now a shipped feature rather than only a seam. `Apply` calls the hook after
the risk gate, a generic HTTP provider is configured from `ROLLOPS_GOVERNANCE_URL`,
and a refusal blocks the deploy rather than escalating it to approval. Unset means no
gate; once set, an unreachable governor also denies. See
`docs/external-governance.md` for the wire contract and the reasoning.
