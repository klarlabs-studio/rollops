package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
)

// legacySchemaVersion is the highest migration that predates the version
// table. Versions 1..11 were applied by re-executing every migration on every
// open; they are recorded as a baseline rather than run again (ADR-0003).
const legacySchemaVersion = 11

// migration is one ordered schema change. Unlike the legacy migrations it runs
// exactly once, so it need not be idempotent.
type migration struct {
	version int
	name    string
	sql     string
}

//go:embed migrations/0012_domain_model.sql
var migration0012 string

// domainMigrations holds the schema for the domain model, starting above the
// legacy baseline.
var domainMigrations = []migration{
	{version: 12, name: "domain_model", sql: migration0012},
}

// applyVersioned brings db up to the last migration in ms. Each migration runs
// in its own transaction together with the row recording it, so a failure
// leaves neither the change nor the claim that it was made.
func applyVersioned(ctx context.Context, db *sql.DB, ms []migration) error {
	if err := validateMigrations(ms); err != nil {
		return err
	}
	if err := ensureMigrationTable(ctx, db); err != nil {
		return err
	}
	applied, err := appliedSet(ctx, db)
	if err != nil {
		return err
	}
	for _, m := range ms {
		if _, done := applied[m.version]; done {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

// baselineLegacySchema records versions 1..11 as applied. It is called only
// after the legacy path has run, so the schema it claims is a fact rather than
// an inference about what an older build might have shipped.
func baselineLegacySchema(ctx context.Context, db *sql.DB) error {
	if err := ensureMigrationTable(ctx, db); err != nil {
		return err
	}
	for v := 1; v <= legacySchemaVersion; v++ {
		if _, err := db.ExecContext(ctx,
			`INSERT OR IGNORE INTO schema_migrations (version, name) VALUES (?, ?)`,
			v, fmt.Sprintf("legacy-%04d", v),
		); err != nil {
			return fmt.Errorf("sqlite: baseline %d: %w", v, err)
		}
	}
	return nil
}

func ensureMigrationTable(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			name       TEXT NOT NULL,
			applied_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`); err != nil {
		return fmt.Errorf("sqlite: create schema_migrations: %w", err)
	}
	return nil
}

func validateMigrations(ms []migration) error {
	previous := legacySchemaVersion
	for _, m := range ms {
		if m.version <= previous {
			return fmt.Errorf(
				"sqlite: migration %q has version %d, which is not above %d",
				m.name, m.version, previous,
			)
		}
		previous = m.version
	}
	return nil
}

func appliedSet(ctx context.Context, db *sql.DB) (map[int]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	applied := map[int]struct{}{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
		}
		applied[v] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	return applied, nil
}

func applyOne(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: migrate %d %q: begin: %w", m.version, m.name, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("sqlite: migrate %d %q: %w", m.version, m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`,
		m.version, m.name,
	); err != nil {
		return fmt.Errorf("sqlite: migrate %d %q: record: %w", m.version, m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: migrate %d %q: commit: %w", m.version, m.name, err)
	}
	return nil
}
