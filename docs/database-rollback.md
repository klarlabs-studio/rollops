# Database Lifecycle (Migrate, Rollback, Gate)

Rollops is not a migration framework: it delegates schema/data changes to the
service's own migration tool (goose, flyway, liquibase, …). It orchestrates
*when* those commands run around a deploy and *whether* a rollback is safe.

Three hooks live under `spec.database`:

```yaml
spec:
  database:
    migrate:
      command: ["goose", "up"]     # forward migration, run at deploy
      timeout: 60s
    rollback:
      command: ["goose", "down"]   # reverse migration, run on rollback
      timeout: 30s
    backwardCompatible: false      # is the new schema safe for the OLD app?
```

- **migrate** — runs once at deploy time, **before** the new manifest is applied,
  so the schema is ready for the new version. A migration failure **aborts the
  deploy**: the target is never touched and the rollout ends `rolled-back`.
- **rollback** — runs on *any* rollback (manual, agent, or auto) after the prior
  manifest is re-applied. Supersedes the deprecated `spec.rollback.database`
  (still honoured as a fallback when `spec.database.rollback` is absent).
- **backwardCompatible** — operator assertion that the migration is safe to run
  the *previous* app version against (expand/contract). Drives the rollback gate
  below.

Each hook's `command` is required and `timeout` is an optional Go duration.

## Rollback gate (backward-compatibility)

A rollback re-applies the old app manifest. If that release ran a forward
migration that is **not** `backwardCompatible` and there is **no** `rollback`
command to reverse the schema, the old app would run against the new schema —
unsafe. Rollops **blocks** such a rollback:

```
engine: rollback blocked: release ran a database migration not declared
backwardCompatible and has no database rollback command; force the rollback to override
```

Override with force:

- CLI: `rollops rollback <target-ref> --force`
- gRPC/REST/UI/MCP: `force: true` on the rollback request

The gate is bypassed automatically on **auto-rollback** (`VerifyOrRollback`): the
deploy already failed, so recovering to the prior state beats leaving the bad
version running.

The gate clears (rollback allowed without force) when either the migration is
declared `backwardCompatible: true`, or a `rollback` command is configured to
reverse it, or the release ran no forward migration at all.

## Rollback command details

Fields (shared shape for `migrate` and `rollback`):

- `command`: executable and arguments. Required.
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
