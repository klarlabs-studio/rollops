package targetv2

import (
	"context"
	"errors"
	"fmt"
)

// Kind is the closed set of failures a target contract distinguishes (§9.5's
// typed error mapping). It is closed because every member is something a
// caller branches on: whether to fall back, whether to retry, whether to ask
// an operator. An error nobody branches on is KindInternal.
type Kind string

const (
	// KindUnsupported means the target does not have this capability. It is
	// not a failure — the call never happened — and it is the one kind that
	// must never be produced by accident, because the caller's response is to
	// carry on without the capability.
	KindUnsupported Kind = "unsupported"

	// KindInvalid means the request is wrong. Retrying it unchanged will fail
	// the same way.
	KindInvalid Kind = "invalid"

	// KindNotFound means the thing the request names is not there.
	KindNotFound Kind = "not_found"

	// KindDenied means the substrate refused on permissions. The target is
	// reachable and working; it is not allowed.
	KindDenied Kind = "denied"

	// KindConflict means the state moved, or an idempotency key was reused
	// with a different request (§9.4). Retrying unchanged is unsafe.
	KindConflict Kind = "conflict"

	// KindUnavailable means the substrate could not be reached or is not
	// ready. Retrying later is the expected response.
	KindUnavailable Kind = "unavailable"

	// KindTimeout means the deadline passed. Whether the work landed is
	// unknown, which is why Apply takes an idempotency key.
	KindTimeout Kind = "timeout"

	// KindCanceled means the caller went away.
	KindCanceled Kind = "canceled"

	// KindInternal is everything else, including anything that did not come
	// from this package. It is the default on purpose: an unrecognised
	// failure read as any other kind would be a wrong branch taken silently.
	KindInternal Kind = "internal"
)

// Error is a target failure with a kind the caller can act on. Message is
// written for a person and crosses the process boundary from a plugin, so it
// is untrusted text: it may be shown and it may be logged, but it is redacted
// on the way to anything durable (INV-012) and nothing parses it.
type Error struct {
	Kind    Kind
	Op      string // the contract method, e.g. "Apply"
	Message string

	// Capability is set when Kind is KindUnsupported.
	Capability Capability
	// IdempotencyKey is set when Kind is KindConflict and the conflict is a
	// replay the provider could not honour.
	IdempotencyKey string

	// Err is the underlying cause. It stays host-side: a plugin's cause
	// arrives as Message, because an error chain does not cross a wire.
	Err error
}

func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = string(e.Kind)
	}
	if e.Op == "" {
		return fmt.Sprintf("target: %s", msg)
	}
	return fmt.Sprintf("target: %s: %s", e.Op, msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Unsupported reports that op needs a capability this target does not have.
// Returning this is how a target declines a call; being absent is not, because
// an absent method cannot be observed across a process boundary (ADR-0006).
func Unsupported(op string, c Capability) error {
	return &Error{
		Kind:       KindUnsupported,
		Op:         op,
		Capability: c,
		Message:    fmt.Sprintf("this target does not support %s", c),
	}
}

// IdempotencyConflict reports that key was replayed with a different request,
// or that the provider cannot guarantee replay (§9.4).
func IdempotencyConflict(op, key string) error {
	return &Error{
		Kind:           KindConflict,
		Op:             op,
		IdempotencyKey: key,
		Message:        fmt.Sprintf("idempotency key %q was already used for a different request", key),
	}
}

// Failf builds a target error of any kind, wrapping cause when there is one.
func Failf(kind Kind, op string, cause error, format string, args ...any) error {
	return &Error{
		Kind:    kind,
		Op:      op,
		Message: fmt.Sprintf(format, args...),
		Err:     cause,
	}
}

// KindOf classifies any error. Context cancellation and deadlines keep their
// own kinds so that §9.5's cancellation and timeout axes are assertable;
// everything unrecognised is KindInternal.
func KindOf(err error) Kind {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return KindCanceled
	case errors.Is(err, context.DeadlineExceeded):
		return KindTimeout
	}
	var te *Error
	if errors.As(err, &te) {
		return te.Kind
	}
	return KindInternal
}

// Abandoned reports the typed refusal owed to a caller who has already gone
// away, or nil while it is still waiting. Every verb checks this before it does
// any work: a target that only notices cancellation when its substrate does
// will finish an aborted call whenever the answer came from cache, and §9.5
// counts that as ignoring the abort.
func Abandoned(ctx context.Context, op string) error {
	err := ctx.Err()
	if err == nil {
		return nil
	}
	return Failf(KindOf(err), op, err, "%s abandoned: %v", op, err)
}

// IsUnsupported reports whether err means the capability is absent rather than
// broken. Callers use it to decide whether to fall back; nothing else may.
func IsUnsupported(err error) bool { return KindOf(err) == KindUnsupported }
