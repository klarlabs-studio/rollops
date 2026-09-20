-- 0017_verification_runs: what the checks said about a deployment (spec 11.5).
--
-- Rollback: DROP TABLE verification_runs. Destructive — this is the only place
-- the reason a verifier gave and the evidence it pointed at are kept. The event
-- log records that a verification completed and with what verdict, but not what
-- an operator would need to go and look at, so an audit asking why a promotion
-- was allowed loses its detail. Back the file up first. Nothing in schema 16
-- references it, so dropping it returns the database to schema 16 exactly.

-- seq is the primary key so that a row's identity and its position in the order
-- runs were recorded are one fact rather than two that could disagree. A
-- deployment can be verified more than once — a paused canary probed again —
-- and which answer came last is the whole question an operator is asking.
-- finished_at cannot serve: two runs settling within the same clock tick would
-- sort arbitrarily, and the ordering is part of the port's contract.
--
-- There is no UPDATE path and no DELETE path in the repository over this table.
-- A run states what was observed in a window that has closed; editing one
-- rewrites an observation, and verifying again is a second run.
--
-- checks holds every verifier's answer as one JSON document rather than a child
-- table. A check is never queried on its own — the run is the unit that is
-- written, read and reasoned about — and splitting it would buy a join for a
-- shape nothing asks about. The verdict is deliberately not a column: it is
-- derived from the checks by Combine, and a stored copy could disagree with the
-- checks it was supposed to summarise with nothing saying which to believe.
--
-- checks is also the one place in this schema that deliberately holds evidence
-- URIs, which INV-012 warns can carry credentials. The line is correctability,
-- not sensitivity: an event is append-only and can never be fixed (INV-013), so
-- a credential that reaches one is there for good, while a run is a record and
-- a record can be dropped. actor is redacted all the same — it comes from an
-- identity provider and nothing in it is worth that risk.
--
-- No foreign key names the deployment. The repository checks the deployment
-- exists before it writes, which reports a missing one in the ports'
-- vocabulary; a constraint could only report it in the driver's. And a run that
-- vanished when its deployment was deleted would be an audit trail that
-- rewrites itself.
CREATE TABLE verification_runs (
    seq           INTEGER PRIMARY KEY AUTOINCREMENT,
    id            TEXT NOT NULL UNIQUE,
    deployment_id TEXT NOT NULL,
    plan_id       TEXT NOT NULL,
    checks        TEXT NOT NULL DEFAULT '[]',  -- JSON array of Check
    started_at    TEXT NOT NULL,               -- RFC3339Nano, UTC
    finished_at   TEXT NOT NULL,               -- RFC3339Nano, UTC
    actor         TEXT NOT NULL                -- JSON Principal, redacted
);

-- The index ends in seq because every read narrows to one deployment and then
-- walks the order: one index serves the whole query rather than leaving a sort
-- behind.
CREATE INDEX idx_verification_runs_deployment
    ON verification_runs(deployment_id, seq);
