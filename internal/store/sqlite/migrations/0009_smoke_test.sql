-- Smoke-test descriptor captured at deploy time so a later manual Verify (and
-- the Promote that follows) can run the same smoke gate as the auto path, which
-- still holds the config. Stores the config.SmokeTest descriptor as JSON
-- ('' / NULL means no smoke test was configured). Applied idempotently: each
-- statement runs separately and "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN smoke_test TEXT NOT NULL DEFAULT '';
