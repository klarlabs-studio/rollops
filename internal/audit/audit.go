// Package audit is the compliance-grade audit trail, built on bolt. Every
// decision, approval, execution, and rollback is recorded structured and
// traceable, with full identity attribution (which agent, human, or CI run) and
// secret redaction at the logging boundary — secret material never reaches the
// trail, only the fact that a secret was used.
package audit

import (
	"io"
	"strings"

	"go.klarlabs.de/bolt"

	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/secrets"
)

// Action names the audited operation.
type Action string

const (
	ActionPlan     Action = "plan"
	ActionApply    Action = "apply"
	ActionApprove  Action = "approve"
	ActionReject   Action = "reject"
	ActionPromote  Action = "promote"
	ActionRollback Action = "rollback"
	ActionDrift    Action = "drift"
	ActionSchedule Action = "schedule"
	// ActionOrphan records a target that stopped being declared. Not a
	// deletion: rollops does not remove what it can no longer describe (#154).
	ActionOrphan Action = "orphan"
	ActionFreeze Action = "freeze"
)

// Entry is one audited event.
type Entry struct {
	Action    Action
	RolloutID string
	TargetRef string
	Phase     string
	Actor     rollout.Identity // full attribution — captured immutably
	Detail    string
	Fields    map[string]any
}

// Logger writes audit entries via bolt and redacts secret material.
type Logger struct {
	l        *bolt.Logger
	redactor *Redactor
}

// New builds an audit logger writing JSON to w.
func New(w io.Writer) *Logger {
	return &Logger{l: bolt.New(bolt.NewJSONHandler(w)), redactor: NewRedactor()}
}

// Redactor returns the logger's redactor so resolved secret values can be
// registered for scrubbing from free-text fields.
func (a *Logger) Redactor() *Redactor { return a.redactor }

// Record writes an audit entry. Strings are scrubbed of registered secret
// values; secrets.Secret values render as "***".
func (a *Logger) Record(e Entry) {
	ev := a.l.Info().
		Str("action", string(e.Action)).
		Str("actor_kind", e.Actor.Kind).
		Str("actor", e.Actor.Name)
	if e.RolloutID != "" {
		ev = ev.Str("rollout_id", e.RolloutID)
	}
	if e.TargetRef != "" {
		ev = ev.Str("target_ref", e.TargetRef)
	}
	if e.Phase != "" {
		ev = ev.Str("phase", e.Phase)
	}
	if e.Detail != "" {
		ev = ev.Str("detail", a.redactor.Scrub(e.Detail))
	}
	for k, v := range e.Fields {
		switch val := v.(type) {
		case secrets.Secret:
			ev = ev.Str(k, "***")
		case string:
			ev = ev.Str(k, a.redactor.Scrub(val))
		default:
			ev = ev.Any(k, v)
		}
	}
	ev.Msg(string(e.Action))
}

// Redactor scrubs known secret values from free text — defense in depth on top
// of the self-redacting Secret type.
type Redactor struct {
	values []string
}

// NewRedactor returns an empty redactor.
func NewRedactor() *Redactor { return &Redactor{} }

// Register records a resolved secret's value so it is scrubbed if it ever
// appears in audited text.
func (r *Redactor) Register(s secrets.Secret) {
	if v := s.Reveal(); v != "" {
		r.values = append(r.values, v)
	}
}

// Scrub replaces any registered secret value in s with "***".
func (r *Redactor) Scrub(s string) string {
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}
