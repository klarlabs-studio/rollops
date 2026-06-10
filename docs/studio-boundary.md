# Studio Boundary

Rollops OSS is a single-workspace, self-hosted rollout operator. Managed
multi-customer orchestration belongs in a studio layer above core.

`internal/studio.Boundary` enforces that line: the empty/default workspace and
`oss` are allowed in core, while named customer workspaces require the managed
studio layer.

The fleet dashboard contract is intentionally just data:

- workspace
- target
- phase
- health
- sync
- risk

Core can expose or test the shape without owning tenancy, billing, customer
isolation, or managed-service policy.
