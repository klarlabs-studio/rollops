-- Tick-driven canary stepper snapshot. Opaque JSON (plan + statekit snapshot +
-- step entered-at) so a restart resumes a mid-pause bake. Empty means the
-- rollout is not tick-driven. Applied idempotently: each statement runs
-- separately and "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN stepper_snap TEXT NOT NULL DEFAULT '';
