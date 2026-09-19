-- 0012_domain_model: the aggregates a release moves through — projects,
-- environments, artifacts and releases (spec 4, 36).
--
-- Rollback: DROP TABLE release_artifacts, releases, artifacts, target_bindings,
-- environments, projects. Destructive; back the file up first. Nothing in the
-- legacy rollout schema references these tables, so dropping them returns the
-- database to schema 11 exactly.
--
-- Mutable aggregates carry a revision for compare-and-set (ADR-0003, spec 18.1).
-- Artifacts and releases carry none: they are immutable after creation
-- (INV-002, INV-003), so there is no update to lose a race.
--
-- Nested collections are stored as JSON on the aggregate row, because they are
-- only ever read as part of it. Target bindings are the exception and get their
-- own table, as spec 36 names one and because a target is addressable on its
-- own: the deployment lock is scoped to environment and target (spec 18.2).

CREATE TABLE projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    labels      TEXT NOT NULL DEFAULT '{}',   -- JSON object
    created_at  TEXT NOT NULL,                -- RFC3339Nano
    updated_at  TEXT NOT NULL,
    revision    INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE environments (
    id             TEXT PRIMARY KEY,
    project_id     TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    kind           TEXT NOT NULL,
    policies       TEXT NOT NULL DEFAULT '[]',  -- JSON array of PolicyBinding
    variables      TEXT NOT NULL DEFAULT '{}',  -- JSON object of value.Ref
    labels         TEXT NOT NULL DEFAULT '{}',
    ttl_seconds    INTEGER NOT NULL DEFAULT 0,  -- 0 means no expiry
    delete_on_close INTEGER NOT NULL DEFAULT 0,
    revision       INTEGER NOT NULL DEFAULT 1,
    UNIQUE (project_id, name)
);

-- position preserves the order the bindings were declared in, so reading an
-- environment back yields the list it was written with rather than one sorted
-- by name.
CREATE TABLE target_bindings (
    environment_id TEXT NOT NULL REFERENCES environments(id) ON DELETE CASCADE,
    name           TEXT NOT NULL,
    driver         TEXT NOT NULL,
    config         TEXT NOT NULL DEFAULT '{}',  -- JSON object of value.Ref
    labels         TEXT NOT NULL DEFAULT '{}',
    position       INTEGER NOT NULL,
    PRIMARY KEY (environment_id, name)
);
CREATE INDEX idx_target_bindings_driver ON target_bindings(driver);

-- digest is the full "algorithm:hex" rendering. Two artifacts in one project
-- with the same kind and digest are the same artifact, so registering one twice
-- is a conflict rather than a second row (INV-002).
CREATE TABLE artifacts (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL,
    digest      TEXT NOT NULL,
    locator     TEXT NOT NULL,
    size        INTEGER NOT NULL DEFAULT 0,
    media_type  TEXT NOT NULL DEFAULT '',
    metadata    TEXT NOT NULL DEFAULT '{}',
    provenance  TEXT NOT NULL DEFAULT 'null',  -- JSON DocumentRef or null
    sboms       TEXT NOT NULL DEFAULT '[]',
    signatures  TEXT NOT NULL DEFAULT '[]',
    created_at  TEXT NOT NULL,
    UNIQUE (project_id, kind, digest)
);
CREATE INDEX idx_artifacts_project ON artifacts(project_id, created_at);

-- fingerprint is derived from the release's content, not stored as its
-- identity: it is indexed so that "have we already built this" is one lookup,
-- and it is recomputed rather than trusted when it matters.
CREATE TABLE releases (
    id          TEXT PRIMARY KEY,
    project_id  TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    version     TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    source      TEXT NOT NULL,                 -- JSON SourceRevision
    provenance  TEXT NOT NULL DEFAULT 'null',
    created_by  TEXT NOT NULL,                 -- JSON Principal, redacted
    created_at  TEXT NOT NULL,
    labels      TEXT NOT NULL DEFAULT '{}',
    annotations TEXT NOT NULL DEFAULT '{}',
    UNIQUE (project_id, version)
);
CREATE INDEX idx_releases_fingerprint ON releases(project_id, fingerprint);
CREATE INDEX idx_releases_created ON releases(project_id, created_at);

-- One artifact per role, and one role per artifact: both are what makes the
-- artifact a deployment selects deterministic rather than order-dependent.
CREATE TABLE release_artifacts (
    release_id  TEXT NOT NULL REFERENCES releases(id) ON DELETE CASCADE,
    role        TEXT NOT NULL,
    artifact_id TEXT NOT NULL REFERENCES artifacts(id),
    PRIMARY KEY (release_id, role),
    UNIQUE (release_id, artifact_id)
);
