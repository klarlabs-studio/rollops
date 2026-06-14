-- Forward migration command and backward-compatibility flag captured at deploy
-- time, so a rollback can be gated: a release that ran a non-backwardCompatible
-- migration with no reverse command is unsafe to roll back without force.
-- db_migrate_cmd is a JSON array of argv (empty or NULL means no forward
-- migration ran). Applied idempotently: each statement runs separately and
-- "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN db_migrate_cmd TEXT NOT NULL DEFAULT '';
ALTER TABLE rollouts ADD COLUMN db_backward_compatible INTEGER NOT NULL DEFAULT 0;
