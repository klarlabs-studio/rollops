-- Progressive-delivery descriptors captured at deploy time so a later rollback
-- (auto, manual, or agent-driven) can reset the delivery plane — shift traffic
-- back to stable and disable the coupled feature flag — without the config in
-- hand. Each column stores the config descriptor as JSON ('' / NULL means that
-- delivery mechanism was not configured). Applied idempotently: each statement
-- runs separately and "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN delivery_traffic TEXT NOT NULL DEFAULT '';
ALTER TABLE rollouts ADD COLUMN delivery_flag TEXT NOT NULL DEFAULT '';
