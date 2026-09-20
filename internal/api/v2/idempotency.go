package apiv2

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/digest"
)

// DefaultKeyLifetime is how long a retry is recognised when the composition
// root does not say. A day covers a client that retries across a restart of
// its own, and bounds how long a key nobody will ever reuse occupies a row.
const DefaultKeyLifetime = 24 * time.Hour

// Operation names scope the key space. Keys are the caller's to invent and
// §18.3 has a CLI generate one per invocation, so two unrelated mutations
// handed the same string must not replay for each other.
const (
	opCreatePlan        = "CreatePlan"
	opApplyPlan         = "ApplyPlan"
	opApproveDeployment = "ApproveDeployment"
	opCancelDeployment  = "CancelDeployment"
	opCreateRelease     = "CreateRelease"
	opCreateProject     = "CreateProject"
)

// fingerprint summarises the request a key was first used for, so that a key
// reused for something else is refused rather than answered.
//
// The parts are joined with a NUL, which no identifier, strategy or decision
// contains. A caller could smuggle one through a free-text reason and collide
// two of their own requests deliberately; that costs them their own replay and
// gains them nothing, so the encoding is not length-prefixed to prevent it.
func fingerprint(parts ...string) digest.Digest {
	return digest.Of([]byte(strings.Join(parts, "\x00")))
}

// once performs a mutation at most once per idempotency key (§9.4, §18.3).
//
// The key is claimed before the work and completed after it, so a retry that
// arrives while the first call is still running sees the claim rather than an
// empty table. perform returns the identity to record alongside its view;
// replay turns a recorded identity back into that view by reading it, because
// the record holds identity only — a stored response body would go stale the
// moment the resource it described moved on (§23.3).
//
// An empty key performs without recording. §18.3 requires a key to be
// supported, not supplied: the protection is for a caller that retries, and a
// caller that cannot retry has nothing to protect.
func once[T any](
	ctx context.Context,
	s *Service,
	op, key string,
	fp digest.Digest,
	perform func(context.Context) (string, T, error),
	replay func(context.Context, string) (T, error),
) (T, error) {
	var zero T
	if key == "" {
		_, out, err := perform(ctx)
		return out, err
	}

	rec, err := s.keys.Get(ctx, op, key)
	switch {
	case err == nil:
		return recall(ctx, s.clock.Now(), op, key, fp, rec, replay)
	case !errors.Is(err, port.ErrNotFound):
		return zero, failure("apiv2: %s: idempotency key %q: %w", op, key, err)
	}

	now := s.clock.Now()
	err = s.keys.Create(ctx, port.IdempotencyRecord{
		Operation:   op,
		Key:         key,
		Fingerprint: fp,
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.keyLifetime),
	})
	switch {
	case errors.Is(err, port.ErrAlreadyExists):
		// Somebody claimed the key between the read above and this write. That
		// is the race the claim exists to settle, so the loser reads what the
		// winner claimed instead of performing.
		rec, err := s.keys.Get(ctx, op, key)
		if err != nil {
			// Not classified by failure, because ErrNotFound here would surface
			// as NOT_FOUND: the key was taken a moment ago and is gone now, and
			// the caller named nothing that could be missing. Telling them a
			// resource was not found would send them looking for one.
			return zero, internal(
				"apiv2: %s: idempotency key %q was claimed and then could not be read: %w",
				op, key, err)
		}
		return recall(ctx, now, op, key, fp, rec, replay)
	case err != nil:
		return zero, failure("apiv2: %s: idempotency key %q: %w", op, key, err)
	}

	id, out, err := perform(ctx)
	if err != nil {
		// The call answered nothing, so the key goes back. A discard that
		// itself fails leaves the claim standing and blocks the retry, but the
		// caller's error is the one they need to see and replacing it with
		// ours would hide why the mutation failed in the first place.
		_ = s.keys.Discard(ctx, op, key)
		return zero, err
	}
	if err := s.keys.Complete(ctx, op, key, id); err != nil {
		return zero, failure("apiv2: %s: idempotency key %q: %w", op, key, err)
	}
	return out, nil
}

// recall answers from a record that already exists, or says why it cannot.
//
// Each refusal is a conflict rather than an invalid argument: the request is
// well formed, and what is wrong is the state the key is in. §9.4 allows
// exactly this where replay cannot be guaranteed.
func recall[T any](
	ctx context.Context,
	now time.Time,
	op, key string,
	fp digest.Digest,
	rec port.IdempotencyRecord,
	replay func(context.Context, string) (T, error),
) (T, error) {
	var zero T
	switch {
	case rec.Fingerprint != fp:
		return zero, conflict(
			"apiv2: %s: idempotency key %q was first used for a different request", op, key)
	case rec.Result == "":
		return zero, conflict(
			"apiv2: %s: a call with idempotency key %q has not returned yet", op, key)
	case !now.Before(rec.ExpiresAt):
		return zero, conflict(
			"apiv2: %s: idempotency key %q is past the window it was recorded under", op, key)
	}
	return replay(ctx, rec.Result)
}

// internal marks a failure of ours that a sentinel in the chain would
// otherwise classify as the caller's. The message the caller gets is the
// generic one apierr.From would have built; the detail stays for the log.
func internal(format string, args ...any) *apierr.Error {
	err := fmt.Errorf(format, args...)
	return &apierr.Error{Code: apierr.Internal, Message: "the request could not be completed", Err: err}
}

// conflict marks a request the caller could reasonably have made against a
// world that will not take it. It builds a fresh Error rather than wrapping a
// sentinel so the message can name which key and which operation.
func conflict(format string, args ...any) *apierr.Error {
	err := fmt.Errorf(format, args...)
	return &apierr.Error{Code: apierr.Conflict, Message: err.Error(), Err: err}
}
