-- 0016_idempotency_keys: what a mutation already answered, so a retry of the
-- same request returns it rather than doing the work twice (spec 18.3).
--
-- Rollback: DROP TABLE idempotency_keys. Not destructive in the way 0015 is —
-- this table records no decision anybody made, only that a particular request
-- was already answered, and every mutation it guards has its own durable
-- record elsewhere. Dropping it loses the replay window, so a retry already in
-- flight would be answered a second time. Nothing in schema 15 references it,
-- so dropping it returns the database to schema 15 exactly.

-- The uniqueness is on (operation, key) rather than on key alone. Keys are the
-- caller's to invent, and a CLI that generates one per invocation has no way
-- to avoid colliding with a key some other mutation was given; scoping by
-- operation means two unrelated calls that happened to pick the same string
-- are two records rather than one replaying for the other.
--
-- fingerprint is a digest of the request the key was first used for. A key
-- reused for a different request is not a retry, and answering it from this
-- table would tell a caller their second, different request had succeeded.
--
-- result is the identity the first call returned, not its response body.
-- Spec 23.3 has a mutation return identity and let the caller read the rest,
-- and a stored body would go stale the moment the resource it described moved
-- on — a replay would then answer accurately about a state that is gone.
--
-- expires_at is stored rather than derived, so a record keeps the window it
-- was written under: changing the default must not retroactively revive keys
-- that had already lapsed. It is NOT NULL because a replay window that never
-- closes is a table that only grows, and unlike an approval there is no case
-- for an open-ended one.
--
-- A row is written in two steps. Create claims the key with result = '', and
-- Complete fills it in; the claim is what a concurrent retry sees while the
-- first call is still running, which is the case a key exists for. The only
-- UPDATE is that one transition, and it names result = '' in its WHERE clause
-- so a completed row can never be rewritten: a record states what one call
-- returned, and editing it would make a replay answer for a request that never
-- happened.
CREATE TABLE idempotency_keys (
    seq         INTEGER PRIMARY KEY AUTOINCREMENT,
    operation   TEXT NOT NULL,
    key         TEXT NOT NULL,
    fingerprint TEXT NOT NULL,             -- algorithm:hex
    result      TEXT NOT NULL,           -- '' until the claimed call returns
    created_at  TEXT NOT NULL,             -- RFC3339Nano, UTC
    expires_at  TEXT NOT NULL,             -- RFC3339Nano, UTC
    UNIQUE (operation, key)
);

-- Every read is a point lookup on the pair, which the UNIQUE constraint
-- already indexes. The only additional index is on expiry, for the sweep that
-- eventually reclaims lapsed rows: without it that sweep is a table scan over
-- the one table whose whole purpose is to be written to on every mutation.
CREATE INDEX idx_idempotency_keys_expiry ON idempotency_keys(expires_at);
