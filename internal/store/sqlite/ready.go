package sqlite

import (
	"context"
	"fmt"
)

// Ready reports whether the store can serve the daemon: the database is
// open, its pages are intact, and the rollouts table is readable.
//
// It backs /readyz. On 2026-09-19 the database went corrupt and /readyz,
// which returned 200 unconditionally, kept the pod Ready while every
// reconcile failed for every repo (#196). PRAGMA quick_check reads every
// page but skips the slower index-consistency checks of integrity_check;
// on a store of a few MB that costs milliseconds per probe.
func (s *Store) Ready(ctx context.Context) error {
	var result string
	if err := s.db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&result); err != nil {
		return fmt.Errorf("sqlite: quick_check: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("sqlite: quick_check: %s", result)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT 1 FROM rollouts LIMIT 1)").Scan(&n); err != nil {
		return fmt.Errorf("sqlite: read rollouts: %w", err)
	}
	return nil
}
