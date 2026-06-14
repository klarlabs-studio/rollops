-- Persist the latest transition note on the rollout row (not just in history),
-- so Status surfaces it to API consumers (gRPC/REST/MCP) the way CLI/UI read it
-- from history. Applied idempotently: "duplicate column" is ignored.
ALTER TABLE rollouts ADD COLUMN note TEXT NOT NULL DEFAULT '';
