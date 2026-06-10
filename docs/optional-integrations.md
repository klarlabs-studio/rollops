# Optional Integrations

Optional integrations are provider seams. Rollops defines the contract and
validation, but core does not require SaaS vendors or external control planes.

## Feature Flags

`internal/featureflags.Hook` accepts an injected provider and validates a flag
change before calling it:

- flag name is required
- rollout percentage must be `0..100`
- nil provider is inert

This supports progressive rollout workflows that coordinate deploys with a
feature flag provider while keeping Rollops provider-agnostic.

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
