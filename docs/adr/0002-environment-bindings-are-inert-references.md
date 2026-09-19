# ADR-0002 — Environment bindings are inert references, not embedded behaviour

- **Status:** accepted
- **Date:** 2026-09-19
- **Spec:** `docs/architecture/rollops-next.md` §4.4, §9, §12, §22.5 · **Backlog:** R2
- **Invariants touched:** INV-007 (infrastructure independence), INV-012 (secret non-persistence)

## Context

Spec §4.4 gives the `Environment` struct four field types it never defines:

```go
Targets     []TargetBinding
Policies    []PolicyBinding
Variables   map[string]ValueRef
Lifecycle   EnvironmentLifecycle
```

`TargetBinding` and `PolicyBinding` appear only in the aggregate diagram
(§4.1) and this struct. Nothing else in the spec describes their shape.

They cannot simply wait for the subsystems that will consume them. R2 needs
the Environment aggregate persisted, and a field added to a persisted
aggregate later is a schema migration — so the shape has to be decided now,
while it is still cheap.

But the subsystems they name are both unbuilt and both explicitly deferred:

- The **Target SDK v2** interface (§9.2) is pending decision #4, which gates
  R6 and Phase D.
- The **Policy Engine** (§12) has a contract but no expression evaluation,
  no risk model wiring, and no decision on how CEL is embedded.

The risk is obvious: define a binding that embeds either subsystem's
vocabulary and the Environment aggregate gets reshaped when that subsystem
lands, taking a migration with it.

## Decision

**A binding names a thing and configures it. It does not contain it.**

Both binding types are inert, comparable, persistable data. Neither holds an
interface value, a client, a connection, or any provider handle. Resolution
from a binding to a working implementation happens in the adapter layer, at
the moment of use.

### TargetBinding

```go
type TargetBinding struct {
    Name   string
    Driver string
    Config map[string]value.Ref
    Labels map[string]string
}
```

- `Name` is unique within the environment. It is what a deployment plan
  names when it says where an operation lands.
- `Driver` is a plain string — `"kubernetes"`, `"ssh"`, `"ftp"` — resolved
  through a registry in the adapter layer. It is deliberately **not** a
  typed enum: targets are plugins (§9.6), and an enum in the domain would
  mean every new plugin edits the domain, which is the coupling INV-007
  exists to prevent. An unknown driver fails loudly at resolution.
- `Config` is `map[string]value.Ref`, not `map[string]string`, so a
  registry password or kubeconfig is a reference the environment can carry
  without the material ever being stored in it (§22.5, INV-012).

The `Target` interface itself (§9.2) is untouched by this ADR. Decision #4
can land whatever contract it likes; a binding that only names a driver and
its configuration does not care what shape the driver's methods take.

### PolicyBinding

```go
type PolicyBinding struct {
    Name string
    Ref  string
    Mode PolicyMode // enforce | warn
}
```

Notably **without** a list of decision points. §12.1 enumerates where the
engine is invoked, and it was tempting to let a binding subscribe to a
subset. §12.3 argues the other way: *"Do not put all policy semantics
directly into YAML parsing."* Where a policy applies is a policy semantic.
It belongs in the expression the policy evaluates, which already receives
the decision point in its `PolicyInput`. A binding that duplicated that
scoping would give two places to express one rule, and they would disagree.

`Mode` is not deferrable. A policy engine with no warn mode cannot be
adopted incrementally — the first policy anyone writes would either block
production or not exist.

### EnvironmentLifecycle

§4.4 says lifecycle "**MAY** include preview TTL and cleanup policy", which
is enough to define directly:

```go
type EnvironmentLifecycle struct {
    TTL           time.Duration // zero means no expiry
    DeleteOnClose bool
}
```

An ephemeral preview environment expires; a production environment does
not. Zero is the safe default in both fields.

## Consequences

- The Environment aggregate can be persisted now, and R6 and §12 can land
  without reshaping it or migrating its table.
- An environment is fully comparable and serializable. Nothing in it is a
  live handle, so it can be read from storage, rendered in `--output json`,
  and diffed, all without touching infrastructure (INV-007).
- The cost is a resolution step and a class of error that only appears at
  use: a binding naming `driver: kubernets` validates fine and fails when a
  deployment reaches it. Mitigation is a registry lookup in `rollops
  validate` rather than a typed field — config validation against installed
  plugins, not against a compiled-in list.
- `Config` being `map[string]value.Ref` means every target driver parses its
  own configuration. That parsing is part of target conformance (§9.5),
  which already requires secret redaction.
- A policy is identified by an opaque `Ref` string. What that string points
  at — a file path, a module name, an OCI reference — is deliberately left
  to §12. Only the binding is decided here.

## Alternatives rejected

- **Wait for R6 and §12.** Correct in principle, but it means either
  blocking R2 entirely or persisting an Environment without its bindings and
  migrating twice. The binding shape is genuinely separable from the
  contracts it references, so waiting buys nothing.
- **`TargetBinding` holds a `Target`.** Makes the aggregate non-serializable
  and non-comparable, drags provider dependencies into the domain, and
  violates INV-007 outright.
- **`Driver` as a typed enum.** Rejected above: it makes the domain the
  registry of every plugin that will ever exist.
- **`Config map[string]string`.** A target needs credentials. Plain strings
  mean credentials in the environment record, which INV-012 forbids.
- **`PolicyBinding` scoped to decision points.** Rejected above: two places
  to express one rule, contradicting §12.3.
