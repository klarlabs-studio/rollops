# Database Rollback

Database rollback is an optional Phase 2 hook for the auto-rollback path. It is
not a migration framework: Rollops re-applies the prior target manifest, then
runs the operator-provided command so the service's own migration tool can
reverse schema or data changes.

## Config

The hook lives under `spec.rollback.database`:

```yaml
rollback:
  auto: true
  smokeTest:
    command: ["./smoke.sh"]
  database:
    command: ["goose", "down"]
    timeout: 30s
```

Fields:

- `command`: executable and arguments. Required when `database` is present.
- `timeout`: optional Go duration for the command.

The command is **captured on the rollout at deploy time**, so every rollback
path runs it — automatic (`VerifyOrRollback`), manual (`rollops rollback
<target-ref>`), or agent-driven. A manual rollback no longer needs the config in
hand: the engine reads the persisted command from the store and runs it after the
manifest re-apply. A rollout deployed without a `database` block carries no
command, so its rollback stays manifest-only — no database intent is inferred.

## Operator Visibility

The rollback history note records `database rollback: succeeded` or
`database rollback: failed: ...`. The UI timeline surfaces the note, and the
one-shot CLI `status <rollout-id>` prints the latest note when history is
available from the local engine.

If the database hook fails, the manifest rollback is still persisted as
`rolled-back`, but verification returns a loud error so operators know the
database state still needs attention.
