# External governance and deployment evidence

Rollops can ask an external governor whether a rollout is permitted, and can report
that a version reached an environment. Both are optional, both are off unless
configured, and neither creates a dependency on any particular governor.

## The constraint that shapes this

Rollops does not depend on the governor, and the governor does not depend on
Rollops. They are separate products with separate release cadences, and a user of
either must never be obliged to adopt the other.

So the integration is a documented wire contract. Rollops implements a **generic
HTTP governance provider** — a URL, a signing secret, a timeout — not a named
integration. Anything that answers the contract works: a governance tool, a policy
service, an internal approval system, or a script.

Concretely, the following are all out of bounds:

- importing a governor's Go packages into this tree
- a `governance.Provider` implementation named after a specific product
- a default configuration that points at one
- a test that cannot run without one

Anything requiring both sides' internals belongs in its own repository, which is
already how targets extend Rollops: `pkg/target` is public so third-party plugins
implement it, and those plugins live outside the core.

## Both flows are Rollops-initiated

```
rollops ──(1) may I deploy version V to environment E? ──▶ governor
        ◀──────── allowed / denied + evidence ───────────
        ──(2) V reached E at T, outcome O ───────────────▶ governor
```

Rollops initiates both. The governor never reaches into a cluster, holds no cluster
credentials, and does not poll.

This also settles a question about evidence quality. A commit changing a manifest is
a *request* to deploy. The controller reporting healthy is the *fact*. An outside
observer watching Git sees only the request, so only Rollops can report the fact —
and a deployment record should say which of the two it represents.

## 1. The gate

`internal/governance` defines the seam:

```go
type Request  struct { Action, TargetRef, Environment, Version string; Actor rollout.Identity }
type Decision struct { Allowed bool; Reason string; Evidence map[string]string }
type Provider interface { Evaluate(ctx context.Context, req Request) (Decision, error) }
```

`Hook.Evaluate` returns `Allowed: true` when no provider is configured. That stays:
a user who has not asked for governance must not be blocked by it.

**A configured provider that cannot be reached denies.** "Governance not requested"
and "governance requested but unavailable" are different states, and giving them the
same outcome means the gate disappears exactly when a rushed deploy is most likely —
during an incident, on a bad network, mid-migration.

`Decision.Evidence` is recorded on the rollout, so the audit trail says *why* a
deployment was allowed — the release ID, the risk score, the approver, the policy
that decided — rather than only that it was.

### Configuring it

Environment variables only, matching how `notify` is wired, so a signing secret
never has to live in a config file that gets committed:

| Variable | Meaning |
|---|---|
| `ROLLOPS_GOVERNANCE_URL` | The governor's endpoint. **Unset means no gate** — this is entirely opt-in. |
| `ROLLOPS_GOVERNANCE_SECRET` | Optional. Signs the request body with HMAC-SHA256 in `X-Rollops-Signature`. |
| `ROLLOPS_GOVERNANCE_TIMEOUT` | Optional Go duration, default `5s`. A mistyped value keeps the default rather than failing startup. |

Both the daemon and the one-shot CLI read these. Wiring only the daemon would leave
`rollops apply` on a laptop as the way around the gate, and a gate you can walk
around is not one.

`rollops doctor` reports reachability, because a fail-closed dependency on the deploy
path should not be discovered during an incident. Its probe sends
`action: "probe"` so a governor can recognize a readiness check and not record it as
a deployment decision.

### The wire contract

Rollops POSTs this before applying, and refuses the apply unless it reads back
`allowed: true`:

```jsonc
// request
{
  "action": "apply",            // "probe" for a doctor readiness check
  "target_ref": "k8s/prod/api",
  "environment": "prod",        // omitted when unknown
  "version": "1.4.0",           // from the rollops.version label; omitted when unset
  "actor_id": "pipeline-7",
  "actor_kind": "ci"            // human | agent | ci
}

// response — HTTP 200 required; any other status is treated as "no decision"
{
  "allowed": false,
  "reason": "release 1.4.0 has no approval on record",
  "evidence": {"release_id": "run-7", "policy": "prod-requires-approval"}
}
```

`reason` is surfaced verbatim to the operator and written to the audit entry, so
write it for someone deciding what to do next. Anything that is not a `200` carrying
parseable JSON — a 500, an HTML error page from a proxy, a timeout — is not a
decision, and denies. Treating a broken governor as permission is the same failure as
treating an unreachable one as permission.

A refusal **blocks**; it does not escalate to approval. The point of delegating a
decision is that the answer is binding — escalating instead would let an approver
here overrule the system that was asked precisely because it knows something Rollops
does not.

### Where the version comes from

`rollops.version` on the config's `metadata.labels`. Version is not first-class in
Rollops: it lives inside a target-specific spec whose shape differs per kind, so this
is a declared fact rather than one extracted by guessing. An absent label sends no
version rather than one inferred from a spec Rollops does not model.

## 2. The evidence

`internal/notify` already posts signed events on `promoted`, `failed`,
`rolled_back` and `approval_needed`, with HMAC-SHA256 in `X-Rollops-Signature`. A
governor that wants deployment evidence subscribes to that webhook like any other
consumer.

`notify.Event` carries `{kind, target_ref, rollout_id, detail}`. It gains
`version` and `environment`, because a deployment record needs the version that
landed and a receiver should not have to call back to understand what it was told.

## Risk scores: two questions, neither recomputed

Rollops scores the **rollout** — which target, what traffic share, what depends on
it, what a failure there costs. A change-governance tool scores the **change** —
what is in it: breaking commits, security touches, who wrote it.

Both are legitimate and they are not the same number. When Rollops asks for a
decision, the governor's score arrives in `Evidence` as a fact to record, not as an
input to re-derive. Two components computing one number for one change is a
reliable source of silent disagreement about which one gates.

## Invariant

This repository must build, test and pass CI with any governor absent, and `go.mod`
must never reference one. That is worth a test asserting the absence: the decision
decays the first time someone reaches for a convenient import.
