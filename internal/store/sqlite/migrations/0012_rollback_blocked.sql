-- Why auto-rollback is disabled for a rollout, decided before it deployed.
-- '' means auto-rollback may run. Set when the recorded rollback target is not
-- what was live (the target was changed outside rollops, or the comparison
-- could not be made), so a failed deploy is left for a human rather than
-- "restored" to a state that was never running. Applied idempotently.
ALTER TABLE rollouts ADD COLUMN rollback_blocked TEXT NOT NULL DEFAULT '';
