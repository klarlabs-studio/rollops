// Package notify delivers rollout notifications — approvals needed, failures,
// rollbacks, promotions — to operators over email (SMTP) or a generic webhook.
// It is a P1 nice-to-have: the engine emits events best-effort, so a flaky
// notifier never blocks or fails a rollout.
package notify

import (
	"context"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
)

// Kind classifies a notification.
type Kind string

const (
	ApprovalNeeded Kind = "approval_needed"
	Failed         Kind = "failed"
	RolledBack     Kind = "rolled_back"
	Promoted       Kind = "promoted"
	Test           Kind = "test" // setup-time channel check (doctor)
)

// Event is a single notification.
type Event struct {
	Kind      Kind   `json:"kind"`
	TargetRef string `json:"target_ref"`
	RolloutID string `json:"rollout_id"`
	Detail    string `json:"detail,omitempty"`

	// Version and Environment describe what reached where.
	//
	// A consumer recording deployment evidence needs both, and this payload carried
	// neither: a target ref identifies where something was deployed to but not what,
	// and a rollout ID is only resolvable by asking us. A receiver should not have to
	// call back to understand what it was told, so the facts travel with the event.
	//
	// Environment comes from the target's declared env and is therefore always
	// present. Version is whatever the operator labelled the deployment with, so it
	// may be empty — empty means unreported, which is honest, rather than a value
	// invented from a target spec whose shape differs per kind.
	Version     string `json:"version,omitempty"`
	Environment string `json:"environment,omitempty"`
}

// verb returns the emoji and human verb for the event kind.
func (e Event) verb() (emoji, verb string) {
	switch e.Kind {
	case ApprovalNeeded:
		return "⏳", "needs approval"
	case Failed:
		return "❌", "failed"
	case RolledBack:
		return "↩️", "rolled back"
	case Promoted:
		return "✅", "promoted"
	default:
		return "•", string(e.Kind)
	}
}

// Message renders a human-readable line for chat notifiers.
func (e Event) Message() string {
	emoji, verb := e.verb()
	msg := fmt.Sprintf("%s Rollops: %s %s", emoji, e.TargetRef, verb)
	if e.RolloutID != "" {
		msg += " (" + e.RolloutID + ")"
	}
	if e.Detail != "" {
		msg += " — " + e.Detail
	}
	return msg
}

// Subject renders an ASCII-safe one-line summary for email subjects.
func (e Event) Subject() string {
	_, verb := e.verb()
	return fmt.Sprintf("Rollops: %s %s", e.TargetRef, verb)
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

// FromEnv wires notifiers from environment variables (ROLLOPS_SMTP_ADDR/
// FROM/TO and optional USER/PASS for email; ROLLOPS_WEBHOOK_URL and optional
// SECRET for the webhook) and returns the configured channel names for
// display. Both return values are nil when nothing is configured. getenv is
// injectable for tests (pass os.Getenv).
func FromEnv(getenv func(string) string) (Notifier, []string) {
	var ns Multi
	var names []string
	if url := getenv("ROLLOPS_BRIEFKASTEN_URL"); url != "" {
		ns = append(ns, Briefkasten{
			URL:   url,
			Token: getenv("ROLLOPS_BRIEFKASTEN_TOKEN"),
			To:    splitRecipients(getenv("ROLLOPS_BRIEFKASTEN_TO")),
		})
		names = append(names, "briefkasten")
	}
	if addr := getenv("ROLLOPS_SMTP_ADDR"); addr != "" {
		em := Email{Addr: addr, From: getenv("ROLLOPS_SMTP_FROM"), To: splitRecipients(getenv("ROLLOPS_SMTP_TO"))}
		if user := getenv("ROLLOPS_SMTP_USER"); user != "" {
			host := addr
			if i := strings.LastIndex(addr, ":"); i >= 0 {
				host = addr[:i]
			}
			em.Auth = smtp.PlainAuth("", user, getenv("ROLLOPS_SMTP_PASS"), host)
		}
		ns = append(ns, em)
		names = append(names, "email")
	}
	if url := getenv("ROLLOPS_WEBHOOK_URL"); url != "" {
		ns = append(ns, Webhook{URL: url, Secret: getenv("ROLLOPS_WEBHOOK_SECRET")})
		names = append(names, "webhook")
	}
	if len(ns) == 0 {
		return nil, nil
	}
	return ns, names
}

// splitRecipients parses a comma-separated recipient list, trimming blanks.
func splitRecipients(s string) []string {
	var to []string
	for r := range strings.SplitSeq(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			to = append(to, r)
		}
	}
	return to
}
