// Package sqlite is the default Store backend: a single-file, pure-Go SQLite
// database (modernc.org/sqlite, no cgo) that keeps the lean single-binary story
// intact. It persists runtime state only — in-flight rollouts, observed
// fingerprints, schedules, and history. Desired state always comes from Git.
//
// Crash safety: every SaveRollout upserts the rollout and appends a history row
// in one transaction, so an interrupted rollout's last durable phase is always
// recoverable and the transition log is never lost.
package sqlite

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/store"
	"go.klarlabs.de/rolloffs/pkg/target"
)

//go:embed migrations/0001_init.sql
var migration0001 string

const timeFormat = time.RFC3339Nano

// Store is the SQLite-backed implementation of store.Store.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path and applies migrations.
// It is safe to call on an existing database; migrations are idempotent.
func Open(path string) (*Store, error) {
	// Busy timeout + WAL keep the single-writer daemon and one-shot CLI from
	// tripping over each other on the same file.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("sqlite: ping: %w", err)
	}
	if _, err := db.Exec(migration0001); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: migrate: %w", err)
	}
	return &Store{db: db}, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// SaveRollout upserts the rollout and appends a history row atomically.
func (s *Store) SaveRollout(ctx context.Context, r rollout.Rollout) error {
	manifest, err := json.Marshal(r.Desired)
	if err != nil {
		return fmt.Errorf("sqlite: marshal manifest: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx, `
		INSERT INTO rollouts (id, target_ref, phase, strategy, manifest, risk_score, initiator_kind, initiator_name, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			phase=excluded.phase, strategy=excluded.strategy, manifest=excluded.manifest,
			risk_score=excluded.risk_score, updated_at=excluded.updated_at`,
		r.ID, r.TargetRef, string(r.Phase), string(r.Strategy), manifest, r.RiskScore,
		r.Initiator.Kind, r.Initiator.Name, r.CreatedAt.Format(timeFormat), r.UpdatedAt.Format(timeFormat))
	if err != nil {
		return fmt.Errorf("sqlite: save rollout: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO history (rollout_id, target_ref, phase, initiator_kind, initiator_name, at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.TargetRef, string(r.Phase), r.Initiator.Kind, r.Initiator.Name, r.UpdatedAt.Format(timeFormat))
	if err != nil {
		return fmt.Errorf("sqlite: append history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
	}
	return nil
}

// LoadRollout retrieves a rollout by id, returning store.ErrNotFound if absent.
func (s *Store) LoadRollout(ctx context.Context, id string) (rollout.Rollout, error) {
	var (
		r                rollout.Rollout
		phase, strat     string
		manifest         []byte
		created, updated string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, target_ref, phase, strategy, manifest, risk_score, initiator_kind, initiator_name, created_at, updated_at
		FROM rollouts WHERE id = ?`, id).
		Scan(&r.ID, &r.TargetRef, &phase, &strat, &manifest, &r.RiskScore, &r.Initiator.Kind, &r.Initiator.Name, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return rollout.Rollout{}, store.ErrNotFound
	}
	if err != nil {
		return rollout.Rollout{}, fmt.Errorf("sqlite: load rollout: %w", err)
	}
	r.Phase = rollout.Phase(phase)
	r.Strategy = rollout.Strategy(strat)
	if err := json.Unmarshal(manifest, &r.Desired); err != nil {
		return rollout.Rollout{}, fmt.Errorf("sqlite: unmarshal manifest: %w", err)
	}
	if r.CreatedAt, err = time.Parse(timeFormat, created); err != nil {
		return rollout.Rollout{}, fmt.Errorf("sqlite: parse created_at: %w", err)
	}
	if r.UpdatedAt, err = time.Parse(timeFormat, updated); err != nil {
		return rollout.Rollout{}, fmt.Errorf("sqlite: parse updated_at: %w", err)
	}
	return r, nil
}

// SaveObservedState upserts the last observed fingerprint for a target.
func (s *Store) SaveObservedState(ctx context.Context, ts rollout.TargetState) error {
	meta, err := json.Marshal(ts.Observed.Meta)
	if err != nil {
		return fmt.Errorf("sqlite: marshal fingerprint meta: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO target_state (target_ref, fingerprint, meta, observed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(target_ref) DO UPDATE SET
			fingerprint=excluded.fingerprint, meta=excluded.meta, observed_at=excluded.observed_at`,
		ts.TargetRef, ts.Observed.Value, meta, ts.ObservedAt.Format(timeFormat))
	if err != nil {
		return fmt.Errorf("sqlite: save observed state: %w", err)
	}
	return nil
}

// ObservedState returns the last observed fingerprint for a target.
func (s *Store) ObservedState(ctx context.Context, targetRef string) (target.Fingerprint, error) {
	var (
		fp   target.Fingerprint
		meta []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT fingerprint, meta FROM target_state WHERE target_ref = ?`, targetRef).
		Scan(&fp.Value, &meta)
	if errors.Is(err, sql.ErrNoRows) {
		return target.Fingerprint{}, store.ErrNotFound
	}
	if err != nil {
		return target.Fingerprint{}, fmt.Errorf("sqlite: observed state: %w", err)
	}
	if len(meta) > 0 {
		if err := json.Unmarshal(meta, &fp.Meta); err != nil {
			return target.Fingerprint{}, fmt.Errorf("sqlite: unmarshal meta: %w", err)
		}
	}
	return fp, nil
}

// Schedule queues a rollout for a future time.
func (s *Store) Schedule(ctx context.Context, sc rollout.ScheduledRollout) error {
	manifest, err := json.Marshal(sc.Desired)
	if err != nil {
		return fmt.Errorf("sqlite: marshal manifest: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO schedules (id, target_ref, due_at, manifest, initiator_kind, initiator_name)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			target_ref=excluded.target_ref, due_at=excluded.due_at, manifest=excluded.manifest,
			initiator_kind=excluded.initiator_kind, initiator_name=excluded.initiator_name`,
		sc.ID, sc.TargetRef, sc.DueAt.Format(timeFormat), manifest, sc.Initiator.Kind, sc.Initiator.Name)
	if err != nil {
		return fmt.Errorf("sqlite: schedule: %w", err)
	}
	return nil
}

// DeleteSchedule removes a schedule by id (no error if it is already gone).
func (s *Store) DeleteSchedule(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id); err != nil {
		return fmt.Errorf("sqlite: delete schedule: %w", err)
	}
	return nil
}

// DueSchedules returns schedules whose DueAt is at or before now, oldest first.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]rollout.ScheduledRollout, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, target_ref, due_at, manifest, initiator_kind, initiator_name
		FROM schedules WHERE due_at <= ? ORDER BY due_at ASC`, now.Format(timeFormat))
	if err != nil {
		return nil, fmt.Errorf("sqlite: due schedules: %w", err)
	}
	defer rows.Close()

	var out []rollout.ScheduledRollout
	for rows.Next() {
		var (
			sc       rollout.ScheduledRollout
			due      string
			manifest []byte
		)
		if err := rows.Scan(&sc.ID, &sc.TargetRef, &due, &manifest, &sc.Initiator.Kind, &sc.Initiator.Name); err != nil {
			return nil, fmt.Errorf("sqlite: scan schedule: %w", err)
		}
		if sc.DueAt, err = time.Parse(timeFormat, due); err != nil {
			return nil, fmt.Errorf("sqlite: parse due_at: %w", err)
		}
		if err := json.Unmarshal(manifest, &sc.Desired); err != nil {
			return nil, fmt.Errorf("sqlite: unmarshal manifest: %w", err)
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// History returns the audit/history records for a target, newest first.
func (s *Store) History(ctx context.Context, targetRef string) ([]rollout.RolloutRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rollout_id, target_ref, phase, note, initiator_kind, initiator_name, at
		FROM history WHERE target_ref = ? ORDER BY seq DESC`, targetRef)
	if err != nil {
		return nil, fmt.Errorf("sqlite: history: %w", err)
	}
	defer rows.Close()

	var out []rollout.RolloutRecord
	for rows.Next() {
		var (
			rec   rollout.RolloutRecord
			phase string
			at    string
		)
		if err := rows.Scan(&rec.RolloutID, &rec.TargetRef, &phase, &rec.Note, &rec.Initiator.Kind, &rec.Initiator.Name, &at); err != nil {
			return nil, fmt.Errorf("sqlite: scan history: %w", err)
		}
		rec.Phase = rollout.Phase(phase)
		if rec.At, err = time.Parse(timeFormat, at); err != nil {
			return nil, fmt.Errorf("sqlite: parse history at: %w", err)
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
