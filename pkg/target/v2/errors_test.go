package targetv2_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

func TestAnUnsupportedCapabilityNamesTheCapability(t *testing.T) {
	// The fallback path is the same code whether a target cannot roll back or
	// cannot be reached, so the error has to say which. "Target failed" is the
	// message that made the v1 bug invisible.
	err := targetv2.Unsupported("Rollback", targetv2.CapabilityNativeRollback)

	var te *targetv2.Error
	if !errors.As(err, &te) {
		t.Fatalf("Unsupported returned %T, want a *targetv2.Error", err)
	}
	if te.Kind != targetv2.KindUnsupported {
		t.Errorf("kind is %q, want %q", te.Kind, targetv2.KindUnsupported)
	}
	if te.Capability != targetv2.CapabilityNativeRollback {
		t.Errorf("capability is %q, want %q", te.Capability, targetv2.CapabilityNativeRollback)
	}
	if msg := err.Error(); !strings.Contains(msg, "Rollback") ||
		!strings.Contains(msg, string(targetv2.CapabilityNativeRollback)) {
		t.Errorf("message %q names neither the call nor the capability", msg)
	}
}

func TestAnUnknownFailureIsNotReadAsUnsupported(t *testing.T) {
	// The whole point: a target that failed and a target that cannot must not
	// collapse into the same answer. Anything unrecognised is internal, which
	// is a failure, and only an explicit Unsupported is a missing capability.
	if targetv2.IsUnsupported(errors.New("connection reset")) {
		t.Errorf("a plain error was read as an unsupported capability")
	}
	if got := targetv2.KindOf(errors.New("connection reset")); got != targetv2.KindInternal {
		t.Errorf("an unrecognised error is %q, want %q", got, targetv2.KindInternal)
	}
	if got := targetv2.KindOf(nil); got != "" {
		t.Errorf("KindOf(nil) is %q, want the empty kind", got)
	}
}

func TestAnIdempotencyConflictCarriesTheKey(t *testing.T) {
	// §9.4: a provider that cannot replay must say so with the key that was
	// replayed, because the caller's next move is to look that key up.
	err := targetv2.IdempotencyConflict("Apply", "dep-7/op-3")

	var te *targetv2.Error
	if !errors.As(err, &te) {
		t.Fatalf("IdempotencyConflict returned %T, want a *targetv2.Error", err)
	}
	if te.Kind != targetv2.KindConflict {
		t.Errorf("kind is %q, want %q", te.Kind, targetv2.KindConflict)
	}
	if te.IdempotencyKey != "dep-7/op-3" {
		t.Errorf("key is %q, want %q", te.IdempotencyKey, "dep-7/op-3")
	}
}

func TestCancellationAndTimeoutKeepTheirOwnKinds(t *testing.T) {
	// Two separate conformance axes (§9.5). They are only assertable if they
	// are distinguishable from each other and from a target that broke.
	if got := targetv2.KindOf(context.Canceled); got != targetv2.KindCanceled {
		t.Errorf("context.Canceled is %q, want %q", got, targetv2.KindCanceled)
	}
	if got := targetv2.KindOf(context.DeadlineExceeded); got != targetv2.KindTimeout {
		t.Errorf("context.DeadlineExceeded is %q, want %q", got, targetv2.KindTimeout)
	}
}

func TestAWrappedKindSurvivesTheWrapping(t *testing.T) {
	// Every layer between the substrate and the engine adds context to the
	// error. A kind that only survives at the innermost frame is a kind the
	// caller never sees.
	wrapped := fmt.Errorf("kubernetes: apply deployment/api: %w",
		targetv2.Unsupported("Prune", targetv2.CapabilityPrune))

	if !targetv2.IsUnsupported(wrapped) {
		t.Errorf("wrapping hid an unsupported capability")
	}
	if got := targetv2.KindOf(wrapped); got != targetv2.KindUnsupported {
		t.Errorf("wrapped kind is %q, want %q", got, targetv2.KindUnsupported)
	}
}

func TestACauseStaysReachable(t *testing.T) {
	cause := errors.New("no such namespace")
	err := targetv2.Failf(targetv2.KindNotFound, "Inspect", cause, "namespace %q", "prod")

	if !errors.Is(err, cause) {
		t.Errorf("the cause is not reachable through errors.Is")
	}
	if got := targetv2.KindOf(err); got != targetv2.KindNotFound {
		t.Errorf("kind is %q, want %q", got, targetv2.KindNotFound)
	}
}

// TestAnAbandonedContextBecomesATypedRefusal covers the guard every verb needs
// before it does any work. A target whose substrate only notices cancellation
// at the next syscall will happily finish a call the operator aborted — worse,
// a call served from cache never reaches a syscall at all.
func TestAnAbandonedContextBecomesATypedRefusal(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()

	for _, tc := range []struct {
		name string
		ctx  context.Context
		want targetv2.Kind
	}{
		{"cancelled", cancelled, targetv2.KindCanceled},
		{"expired", expired, targetv2.KindTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := targetv2.Abandoned(tc.ctx, "Apply")
			if got := targetv2.KindOf(err); got != tc.want {
				t.Fatalf("kind is %q, want %q", got, tc.want)
			}
			var te *targetv2.Error
			if !errors.As(err, &te) || te.Op != "Apply" {
				t.Errorf("the refusal does not name the verb it refused: %v", err)
			}
		})
	}
}

// TestALiveContextIsNotRefused is the other half: the guard is on the hot path
// of every verb, so it has to be silent when the caller is still waiting.
func TestALiveContextIsNotRefused(t *testing.T) {
	if err := targetv2.Abandoned(context.Background(), "Apply"); err != nil {
		t.Fatalf("a live context was refused: %v", err)
	}
}
