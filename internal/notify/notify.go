// Package notify delivers rollout notifications — approvals needed, failures,
// rollbacks, promotions — to operators over Telegram or a generic webhook. It
// is a P1 nice-to-have: the engine emits events best-effort, so a flaky
// notifier never blocks or fails a rollout.
package notify

import (
	"context"
	"fmt"
	"net/http"
)

// Kind classifies a notification.
type Kind string

const (
	ApprovalNeeded Kind = "approval_needed"
	Failed         Kind = "failed"
	RolledBack     Kind = "rolled_back"
	Promoted       Kind = "promoted"
)

// Event is a single notification.
type Event struct {
	Kind      Kind   `json:"kind"`
	TargetRef string `json:"target_ref"`
	RolloutID string `json:"rollout_id"`
	Detail    string `json:"detail,omitempty"`
}

// Message renders a human-readable line for chat notifiers.
func (e Event) Message() string {
	var emoji, verb string
	switch e.Kind {
	case ApprovalNeeded:
		emoji, verb = "⏳", "needs approval"
	case Failed:
		emoji, verb = "❌", "failed"
	case RolledBack:
		emoji, verb = "↩️", "rolled back"
	case Promoted:
		emoji, verb = "✅", "promoted"
	default:
		emoji, verb = "•", string(e.Kind)
	}
	msg := fmt.Sprintf("%s Rolloffs: %s %s", emoji, e.TargetRef, verb)
	if e.RolloutID != "" {
		msg += " (" + e.RolloutID + ")"
	}
	if e.Detail != "" {
		msg += " — " + e.Detail
	}
	return msg
}

// Notifier delivers an event.
type Notifier interface {
	Notify(ctx context.Context, e Event) error
}

// httpDoer is the slice of http.Client the notifiers need (injectable in tests).
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// Multi fans an event out to several notifiers; it aggregates nothing and
// returns the first error, but each is attempted.
type Multi []Notifier

// Notify delivers to every notifier.
func (m Multi) Notify(ctx context.Context, e Event) error {
	var first error
	for _, n := range m {
		if err := n.Notify(ctx, e); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Noop discards events (default).
type Noop struct{}

// Notify does nothing.
func (Noop) Notify(context.Context, Event) error { return nil }
