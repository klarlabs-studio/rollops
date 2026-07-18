# Verify — dry-running the post-deploy gate

`verify` answers one question: **would this rollout promote right now?**

It runs the same post-deploy gate the automatic path and `promote` run — target
health, the smoke test, and (when enabled) metric analysis — reports each gate,
and changes nothing. No phase transition, no promotion, no rollback, no history
entry.

```sh
rollops verify ro-7f31c2
```

```
rollout ro-7f31c2: verifying
health		pass	healthy
smoke		pass
analysis	skipped	no metric analysis configured
verify: ok (nothing changed)
```

A failing gate prints the whole list and exits non-zero, so it composes:

```sh
rollops verify ro-7f31c2 && rollops promote ro-7f31c2
```

```
rollout ro-7f31c2: verifying
health		pass	healthy
smoke		fail	smoke test exit 1 (expected 0)
analysis	not-run
Error: verify: smoke test exit 1 (expected 0)
```

## "Dry run" means nothing is *changed*, not that nothing is *run*

The gates really execute. A configured smoke test runs its command on the daemon
host (under the same confinement policy as the automatic path), and metric
analysis queries the metrics backend. What a dry run guarantees is that no
rollout state is written.

This is why `verify` is authorized as **promote** permission, not a read
permission — a view-only caller must not be able to trigger command execution on
the daemon host.

## Gate statuses

| Status    | Meaning                                                     |
| --------- | ----------------------------------------------------------- |
| `pass`    | The gate ran and passed.                                     |
| `fail`    | The gate ran and failed. The first one sets the verdict.     |
| `skipped` | Not configured (or metric analysis is not enabled).          |
| `not-run` | Short-circuited by an earlier failure — it never executed.   |

Gates run in a fixed order — **health → smoke → analysis** — and stop at the
first failure, exactly as the automatic path does. That is deliberate: a dry run
that kept going past a failure would not predict the real verification. Gates
that never ran are reported as `not-run` rather than omitted, so the report can
never imply a gate passed when it did not execute.

## Where the gate config comes from

The descriptors are captured **on the rollout at deploy time**, not re-read from
config. A verification therefore checks the rollout as it was actually deployed,
and works from any surface — including ones with no checkout at hand.

Rollouts deployed before this capture existed report their smoke and analysis
gates as `skipped`; the health gate always runs.

## Failing closed

An unreadable captured descriptor is an *error*, not a passing gate — a gate
that cannot be read is not a pass. A failing gate, by contrast, is a normal
result: the report comes back with `ok: false` and a reason (HTTP 200, not an
error status).

## Surfaces

| Surface  | Call                                        |
| -------- | ------------------------------------------- |
| CLI      | `rollops verify <rollout-id>`               |
| HTTP API | `POST /v1/verify` `{"id": "<rollout-id>"}`  |
| gRPC     | `RolloutService.Verify`                     |
| MCP      | `rollouts.verify`                           |

All four run the identical engine code path.

## Relationship to `promote`

**`verify` is `promote`'s dry run.** They enforce the same gates, in the same
order, from the same captured descriptors — one code path, two entry points. If
`verify` passes, `promote` passes; if `verify` fails, `promote` refuses.

```sh
rollops promote ro-7f31c2
```

```
Error: engine: promote: health check failed: 503; force the promote to override
```

The rollout stays in `verifying`. Nothing partial happened.

### Forcing past a gate

When the gate itself is wrong — a flaky probe, a metrics backend that is down,
a smoke test broken by something unrelated — override it:

```sh
rollops promote ro-7f31c2 --force
```

The bypass is **never silent**. It is recorded twice:

- on the rollout's note: `promote: post-deploy gates bypassed (forced)`
- in the audit trail: `promote … promoted (post-deploy gates bypassed: forced)`,
  attributed to the identity that forced it

| Surface  | Override                                       |
| -------- | ---------------------------------------------- |
| CLI      | `rollops promote <id> --force` (or `-f`)       |
| HTTP API | `POST /v1/promote` `{"id": "...", "force": true}` |
| gRPC     | `RolloutService.Promote` with `force: true`    |
| MCP      | `rollouts.promote` with `force: true`          |

This mirrors `rollback --force`, which overrides the migration
backward-compatibility gate the same way.

**The web console never forces.** It always promotes with the gates enforced.
Overriding is deliberate break-glass work, and it stays somewhere you have to
type a flag rather than click a button.
