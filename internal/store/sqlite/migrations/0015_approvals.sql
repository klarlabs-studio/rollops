-- 0015_approvals: who answered for what, and which revision they answered
-- about (spec 13).
--
-- Rollback: DROP TABLE approvals. Destructive — this is the only record of
-- which human vouched for which plan, and an audit asking why a production
-- change was allowed has nowhere else to look. Back the file up first. Nothing
-- in schema 14 references it, so dropping it returns the database to schema 14
-- exactly.

-- seq is the primary key so that a row's identity and its position in the
-- order approvals were given are one fact rather than two that could disagree.
-- AUTOINCREMENT rather than a plain rowid: who answered first is part of the
-- record, and a reused position would reorder it. created_at cannot serve —
-- two approvers answering within the same clock tick would sort arbitrarily.
--
-- There is no UPDATE path and no DELETE path in the repository over this
-- table. An approval is a statement somebody made at a moment; editing one
-- rewrites what they said, and withdrawing one is a new approval recording the
-- withdrawal rather than the disappearance of the old.
--
-- No foreign key names the subject, for two reasons. The reference is
-- polymorphic — spec 13 approves plans, deployments and releases through one
-- record — and a constraint cannot span three tables. And an approval that
-- vanished when its project was deleted would be an audit trail that rewrites
-- itself, which is the failure INV-013 forbids. subject_id is therefore plain
-- text, exactly as the event log stores its aggregate id.
--
-- expires_at is nullable rather than defaulted: "does not expire on its own"
-- and "expired at the epoch" are opposite facts, and a default would make an
-- open-ended approval read as one that has already lapsed.
CREATE TABLE approvals (
    seq              INTEGER PRIMARY KEY AUTOINCREMENT,
    id               TEXT NOT NULL UNIQUE,
    subject_kind     TEXT NOT NULL,
    subject_id       TEXT NOT NULL,
    subject_revision TEXT NOT NULL,
    principal        TEXT NOT NULL,             -- JSON Principal, redacted
    decision         TEXT NOT NULL,
    reason           TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL,             -- RFC3339Nano, UTC
    expires_at       TEXT                       -- RFC3339Nano, NULL = no expiry
);

-- The index ends in seq because every read narrows to one subject and then
-- walks the order: one index serves the whole query rather than leaving a sort
-- behind. The revision is deliberately not in the key — a caller asks for
-- every answer about a subject and lets policy decide which revisions still
-- count, so that an approval of a superseded plan is visible as ignored rather
-- than missing.
CREATE INDEX idx_approvals_subject ON approvals(subject_kind, subject_id, seq);
