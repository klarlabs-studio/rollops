-- Metric-analysis descriptor captured at deploy time so a later manual Verify
-- (and the Promote that follows) can run the same metric-analysis gate as the
-- auto path, which still holds the config. Stores the config.Analysis descriptor
-- as JSON ('' / NULL means no analysis was configured). Applied idempotently:
-- each statement runs separately and "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN analysis TEXT NOT NULL DEFAULT '';
