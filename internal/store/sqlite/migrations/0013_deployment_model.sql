-- 0013_deployment_model: what was planned and what happened when it ran —
-- deployment plans and deployments (spec 4.6, 4.7).
--
-- Rollback: DROP TABLE deployments, plans. Destructive; back the file up first.
-- Nothing in schema 12 references either table, so dropping them returns the
-- database to schema 12 exactly.
--
-- A plan has no revision column. It is immutable by design, and the hash is
-- what proves it: a plan that could be updated would be the tamper path the
-- hash exists to detect. A deployment does change as it runs, so it carries a
-- revision for compare-and-set (ADR-0003).

-- Operations, the policy decision and the rollback are stored as JSON on the
-- row because a plan is only ever read whole: nothing queries one operation.
--
-- The value of a change marked sensitive is never written here (INV-011). The
-- plan's own hash excludes those values for exactly this reason, so a redacted
-- plan read back out still verifies against the hash it was approved under.
CREATE TABLE plans (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    release_id     TEXT NOT NULL REFERENCES releases(id),
    base_revision  INTEGER NOT NULL,
    strategy       TEXT NOT NULL,
    operations     TEXT NOT NULL DEFAULT '[]',   -- JSON array of PlannedOperation
    policy         TEXT NOT NULL,                -- JSON Decision, risk inside it
    rollback_plan  TEXT NOT NULL DEFAULT 'null', -- JSON RollbackPlan or null
    created_by     TEXT NOT NULL,                -- JSON Principal, redacted
    created_at     TEXT NOT NULL,                -- RFC3339Nano
    expires_at     TEXT NOT NULL,
    hash           TEXT NOT NULL                 -- "algorithm:hex"
);
CREATE INDEX idx_plans_environment ON plans(environment_id, created_at);

-- started_at and finished_at are nullable rather than defaulted, because "has
-- not started" and "started at the epoch" are different facts and an operator
-- reading a timeline has to be able to tell them apart.
--
-- previous_id references this same table: it is the deployment this one
-- replaces, which is what makes a rollback target derivable from the record.
CREATE TABLE deployments (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    release_id     TEXT NOT NULL REFERENCES releases(id),
    plan_id        TEXT NOT NULL REFERENCES plans(id),
    strategy       TEXT NOT NULL,
    status         TEXT NOT NULL,
    trigger_type   TEXT NOT NULL,
    trigger_detail TEXT NOT NULL DEFAULT '',
    actor          TEXT NOT NULL,                -- JSON Principal, redacted
    created_at     TEXT NOT NULL,
    started_at     TEXT,
    finished_at    TEXT,
    previous_id    TEXT REFERENCES deployments(id),
    revision       INTEGER NOT NULL DEFAULT 1
);

-- The listing index is ordered the way the contract reads them: an environment's
-- deployments, most recent first, because the first row is how a caller asks
-- what is currently deployed.
CREATE INDEX idx_deployments_environment ON deployments(environment_id, created_at DESC, id DESC);

-- status is indexed so that finding the environment's live deployment does not
-- walk its whole history. Which statuses count as finished is not written into
-- the schema: the domain owns that vocabulary and the query supplies it, so
-- adding a status is a change in one place rather than two.
CREATE INDEX idx_deployments_status ON deployments(environment_id, status);
