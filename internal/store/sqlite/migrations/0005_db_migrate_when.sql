-- Forward-migration timeout and timing captured at deploy time, so a deferred
-- post-promote migration can run from the persisted rollout (the config is no
-- longer in hand at promote). db_migrate_when is pre-deploy (default) or
-- post-promote. Applied idempotently: each statement runs separately and
-- "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN db_migrate_timeout TEXT NOT NULL DEFAULT '';
ALTER TABLE rollouts ADD COLUMN db_migrate_when TEXT NOT NULL DEFAULT '';
