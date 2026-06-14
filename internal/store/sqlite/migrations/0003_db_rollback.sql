-- Database rollback command captured at deploy time, so a manual or agent-driven
-- rollback can reverse the database too, not only the auto-rollback path that
-- still holds the config. db_rollback_cmd is a JSON array of argv (empty or NULL
-- means no hook). Applied idempotently: each statement runs separately and
-- "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN db_rollback_cmd TEXT NOT NULL DEFAULT '';
ALTER TABLE rollouts ADD COLUMN db_rollback_timeout TEXT NOT NULL DEFAULT '';
