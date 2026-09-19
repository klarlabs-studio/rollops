package porttest

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/digest"
)

// oneKey is the only key these tests use. Each subtest starts from an empty
// repository, so what distinguishes two records is never the key itself — it
// is whether they were claimed under the same operation.
const oneKey = "k-1"

func record(result, body string, at time.Time) port.IdempotencyRecord {
	return port.IdempotencyRecord{
		Operation:   "ApplyPlan",
		Key:         oneKey,
		Fingerprint: digest.Of([]byte(body)),
		Result:      result,
		CreatedAt:   at,
		ExpiresAt:   at.Add(24 * time.Hour),
	}
}

func runIdempotency(t *testing.T, newRepos Factory) {
	ctx := context.Background()
	at := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	t.Run("a remembered answer reads back whole", func(t *testing.T) {
		r := newRepos(t)
		want := record("dep_0199a0dd-0000-7000-8000-000000000001", `{"plan":"pln_1"}`, at)
		if err := r.Idempotency.Create(ctx, want); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := r.Idempotency.Get(ctx, want.Operation, want.Key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Result != want.Result {
			t.Errorf("result = %q, want %q; a retry would be told a different deployment ran", got.Result, want.Result)
		}
		if got.Fingerprint != want.Fingerprint {
			t.Errorf("fingerprint = %s, want %s; the key could not be checked against the request", got.Fingerprint, want.Fingerprint)
		}
		if !got.CreatedAt.Equal(want.CreatedAt) || !got.ExpiresAt.Equal(want.ExpiresAt) {
			t.Errorf("window = %s..%s, want %s..%s", got.CreatedAt, got.ExpiresAt, want.CreatedAt, want.ExpiresAt)
		}
	})

	// This is what makes two concurrent retries settle rather than both doing
	// the work: the loser is told the key is taken and reads what the winner
	// recorded.
	t.Run("a key cannot be claimed twice within one operation", func(t *testing.T) {
		r := newRepos(t)
		first := record("dep_0199a0dd-0000-7000-8000-000000000001", `{"plan":"pln_1"}`, at)
		if err := r.Idempotency.Create(ctx, first); err != nil {
			t.Fatalf("Create: %v", err)
		}

		second := record("dep_0199a0dd-0000-7000-8000-000000000002", `{"plan":"pln_2"}`, at)
		err := r.Idempotency.Create(ctx, second)

		if !errors.Is(err, port.ErrAlreadyExists) {
			t.Fatalf("Create = %v, want ErrAlreadyExists", err)
		}
		got, err := r.Idempotency.Get(ctx, first.Operation, first.Key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Result != first.Result {
			t.Errorf("result = %q, want the first writer's %q", got.Result, first.Result)
		}
	})

	// Keys are the caller's to invent, and a CLI generating one per invocation
	// has no way to know another operation already used it.
	t.Run("one key means different things to different operations", func(t *testing.T) {
		r := newRepos(t)
		applied := record("dep_0199a0dd-0000-7000-8000-000000000001", `{"plan":"pln_1"}`, at)
		planned := record("pln_0199a0dd-0000-7000-8000-000000000001", `{"release":"rel_1"}`, at)
		planned.Operation = "CreatePlan"

		if err := r.Idempotency.Create(ctx, applied); err != nil {
			t.Fatalf("Create apply: %v", err)
		}
		if err := r.Idempotency.Create(ctx, planned); err != nil {
			t.Fatalf("Create plan: %v", err)
		}

		got, err := r.Idempotency.Get(ctx, "CreatePlan", oneKey)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Result != planned.Result {
			t.Errorf("result = %q, want %q; the operations share a key space", got.Result, planned.Result)
		}
	})

	t.Run("a key nobody has used is not found", func(t *testing.T) {
		r := newRepos(t)

		_, err := r.Idempotency.Get(ctx, "ApplyPlan", oneKey)

		if !errors.Is(err, port.ErrNotFound) {
			t.Fatalf("Get = %v, want ErrNotFound", err)
		}
	})

	// An expired record still reads back. Expiry is the caller's to judge, and
	// a repository that hid a lapsed record would turn a stale retry into a
	// second deployment rather than into a refusal.
	t.Run("a lapsed record is returned rather than hidden", func(t *testing.T) {
		r := newRepos(t)
		lapsed := record("dep_0199a0dd-0000-7000-8000-000000000001", `{"plan":"pln_1"}`, at)
		lapsed.ExpiresAt = at.Add(-time.Hour)
		if err := r.Idempotency.Create(ctx, lapsed); err != nil {
			t.Fatalf("Create: %v", err)
		}

		got, err := r.Idempotency.Get(ctx, lapsed.Operation, lapsed.Key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if !got.ExpiresAt.Equal(lapsed.ExpiresAt) {
			t.Errorf("expires at = %s, want %s", got.ExpiresAt, lapsed.ExpiresAt)
		}
	})
}
