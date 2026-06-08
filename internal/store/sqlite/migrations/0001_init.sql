-- 0001_init: runtime state for Rolloffs. Git holds desired state; these tables
-- hold observed state, in-flight rollouts, schedules, and history only.

CREATE TABLE IF NOT EXISTS rollouts (
    id             TEXT PRIMARY KEY,
    target_ref     TEXT NOT NULL,
    phase          TEXT NOT NULL,
    strategy       TEXT NOT NULL,
    manifest       BLOB NOT NULL,          -- JSON-encoded target.Manifest
    risk_score     REAL NOT NULL DEFAULT 0,
    initiator_kind TEXT NOT NULL DEFAULT '',
    initiator_name TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL,          -- RFC3339Nano
    updated_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_rollouts_target ON rollouts(target_ref);

-- One row per target: the last observed fingerprint, for drift detection.
CREATE TABLE IF NOT EXISTS target_state (
    target_ref  TEXT PRIMARY KEY,
    fingerprint TEXT NOT NULL,
    meta        BLOB NOT NULL DEFAULT '{}',  -- JSON map (never secrets)
    observed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS schedules (
    id             TEXT PRIMARY KEY,
    target_ref     TEXT NOT NULL,
    due_at         TEXT NOT NULL,           -- RFC3339Nano
    manifest       BLOB NOT NULL,
    initiator_kind TEXT NOT NULL DEFAULT '',
    initiator_name TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules(due_at);

-- Append-only audit/history: one row per persisted rollout transition.
CREATE TABLE IF NOT EXISTS history (
    seq            INTEGER PRIMARY KEY AUTOINCREMENT,
    rollout_id     TEXT NOT NULL,
    target_ref     TEXT NOT NULL,
    phase          TEXT NOT NULL,
    note           TEXT NOT NULL DEFAULT '',
    initiator_kind TEXT NOT NULL DEFAULT '',
    initiator_name TEXT NOT NULL DEFAULT '',
    at             TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_history_target ON history(target_ref, seq DESC);
