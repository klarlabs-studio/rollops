package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// querier is the part of *sql.DB and *sql.Tx the domain repositories use. It
// exists so that one query body serves both, which is what lets a repository
// method work the same inside and outside a transaction.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type txKey struct{}

func txFrom(ctx context.Context) (*sql.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(*sql.Tx)
	return tx, ok
}

// WithinTransaction runs fn inside one transaction and commits only if it
// returns nil. The transaction travels on the derived context, so no repository
// signature mentions database/sql (ADR-0003).
//
// Nesting reuses the open transaction rather than opening a savepoint: partial
// rollback of half a command is not a behaviour any use case has asked for, and
// simulating it invites the bug it looks like it prevents.
func (s *Store) WithinTransaction(ctx context.Context, fn func(context.Context) error) error {
	if _, open := txFrom(ctx); open {
		return fn(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlite: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlite: commit: %w", err)
	}
	return nil
}

// conn returns the open transaction if there is one and the pool otherwise. A
// read may fall back to the pool; a write must not, which is why every write
// below goes through WithinTransaction rather than calling this directly.
func (s *Store) conn(ctx context.Context) querier {
	if tx, open := txFrom(ctx); open {
		return tx
	}
	return s.db
}
