-- 0010_freeze: persist the emergency kill-switch so a daemon restart does not
-- silently lift it. Runtime state only — one row, id=1.

CREATE TABLE IF NOT EXISTS runtime_freeze (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    active  INTEGER NOT NULL DEFAULT 0,
    reason  TEXT NOT NULL DEFAULT '',
    by_kind TEXT NOT NULL DEFAULT '',
    by_name TEXT NOT NULL DEFAULT '',
    at      TEXT NOT NULL DEFAULT ''
);
