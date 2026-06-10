-- Progressive step progress, persisted per health-gated step so operator
-- surfaces can show live "canary 2/3 (50%)" state. Applied idempotently:
-- each statement is executed separately and "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN step_index INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rollouts ADD COLUMN step_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rollouts ADD COLUMN step_weight INTEGER NOT NULL DEFAULT 0;
