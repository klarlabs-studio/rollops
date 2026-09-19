package policy

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
)

var (
	// ErrRequirementUnmet reports a condition the decision attached that
	// nothing has discharged yet. It is not a refusal: the gate is waiting.
	ErrRequirementUnmet = errors.New("policy: requirement not satisfied")

	// ErrApprovalDenied reports a principal who refused this exact plan.
	ErrApprovalDenied = errors.New("policy: an approver rejected this revision")

	// ErrDecisionRefuses reports approvals offered against a decision that did
	// not allow the operation. Conditions can be discharged; a refusal cannot.
	ErrDecisionRefuses = errors.New("policy: decision does not allow the operation")
)

// SubjectRef names what an approval is about. Revision is the load-bearing
// field: §13 requires an approval to bind to the exact plan it approved, and a
// reference without one identifies a moving target rather than a decision.
type SubjectRef struct {
	Kind     string // "plan", "deployment", "release"
	ID       string
	Revision string
}

// sameAs reports whether two references name the same thing in the same state.
// The zero value matches nothing, so an approval that never recorded what it
// approved discharges nothing.
func (s SubjectRef) sameAs(other SubjectRef) bool {
	if s.Kind == "" || s.ID == "" || s.Revision == "" {
		return false
	}
	return s == other
}

func (s SubjectRef) String() string { return s.Kind + " " + s.ID + "@" + s.Revision }

// ApprovalDecision is what a principal said. It is a tri-state rather than a
// bool because the zero value has to be neither: an approval record that was
// built and never filled must not read as a yes.
type ApprovalDecision string

const (
	ApprovalGranted ApprovalDecision = "granted"
	ApprovalDenied  ApprovalDecision = "denied"
)

// Approval is one principal's answer about one revision of one subject (§13).
//
// It carries no authority of its own — it discharges a Requirement that a
// Decision attached, and only for the revision named in Subject. An approval
// that outlives the plan it approved would authorize a change its approver
// never read, which is the failure SatisfiedBy exists to prevent.
type Approval struct {
	ID        identity.ApprovalID
	Subject   SubjectRef
	Principal identity.Principal
	Decision  ApprovalDecision
	Reason    string
	CreatedAt time.Time
	// ExpiresAt bounds how long the answer stands. Zero means it does not
	// expire on its own — the plan moving is what invalidates it.
	ExpiresAt time.Time
}

// Validate reports whether the approval is a complete, auditable record.
func (a Approval) Validate() error {
	if a.Subject.Kind == "" || a.Subject.ID == "" || a.Subject.Revision == "" {
		return fmt.Errorf("policy: approval %s does not say what it approved", a.ID)
	}
	if err := a.Principal.Validate(); err != nil {
		return fmt.Errorf("policy: approval %s: %w", a.ID, err)
	}
	switch a.Decision {
	case ApprovalGranted:
	case ApprovalDenied:
		// The decision half of ErrUnexplainedDecision: whoever is stopped by
		// this rejection has to be told what stopped them.
		if strings.TrimSpace(a.Reason) == "" {
			return fmt.Errorf("%w: approval %s", ErrUnexplainedDecision, a.ID)
		}
	default:
		return fmt.Errorf("policy: approval %s records no decision", a.ID)
	}
	if a.CreatedAt.IsZero() {
		return fmt.Errorf("policy: approval %s is not dated", a.ID)
	}
	return nil
}

// counts reports whether this approval still stands for the given subject at
// the given moment.
func (a Approval) counts(subject SubjectRef, at time.Time) bool {
	if !a.Subject.sameAs(subject) {
		return false
	}
	return a.ExpiresAt.IsZero() || at.Before(a.ExpiresAt)
}

// UnmetError names the condition still standing between a plan and an apply,
// and how far short of it the approvals fall. "Not satisfied" on its own makes
// an operator guess which gate is holding the deployment.
type UnmetError struct {
	Requirement Requirement
	Subject     SubjectRef
	Have        int
	Want        int
}

func (e *UnmetError) Error() string {
	base := fmt.Sprintf("%v: %s for %s", ErrRequirementUnmet, e.Requirement.Type, e.Subject)
	if e.Requirement.Role != "" {
		base += " by " + e.Requirement.Role
	}
	if e.Want > 0 {
		base += fmt.Sprintf(" (have %d of %d)", e.Have, e.Want)
	}
	return base
}

func (e *UnmetError) Unwrap() error { return ErrRequirementUnmet }

// DeniedError reports the principal who rejected this exact revision.
type DeniedError struct {
	Approval Approval
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("%v: %s: %s", ErrApprovalDenied, e.Approval.Principal.ID, e.Approval.Reason)
}

func (e *DeniedError) Unwrap() error { return ErrApprovalDenied }

// SatisfiedBy reports why the decision does not yet permit an apply of this
// exact subject revision, or nil if it does.
//
// This is the method that turns StatusAwaitingApproval back into a deployment
// that may proceed, and the one place §13's binding rule is enforced: an
// approval of another revision is not an approval of this one. Re-planning
// therefore invalidates approvals by construction rather than by remembering to
// clear them, because the plan hash is part of what was approved.
//
// Requirements other than approval are deliberately never discharged here. A
// signed artifact is not something a human can vouch for, and reporting one as
// met because somebody clicked approve would make the gate decoration.
func (d Decision) SatisfiedBy(subject SubjectRef, approvals []Approval, at time.Time) error {
	if !d.Allowed {
		return ErrDecisionRefuses
	}
	// A rejection of this revision is decisive: it is not outvoted by whoever
	// approved alongside it, and it is not waiting for anything.
	for _, a := range approvals {
		if a.Decision == ApprovalDenied && a.counts(subject, at) {
			return &DeniedError{Approval: a}
		}
	}
	for _, r := range d.Requirements {
		if r.Type != RequireApproval {
			return &UnmetError{Requirement: r, Subject: subject}
		}
		have := distinctApprovers(subject, approvals, r, at)
		if have < r.Count {
			return &UnmetError{Requirement: r, Subject: subject, Have: have, Want: r.Count}
		}
	}
	return nil
}

// distinctApprovers counts the principals who granted this revision and hold
// the role the requirement asks for. Distinct is the point: counting records
// rather than people would let one approver satisfy a four-eyes rule by
// answering twice.
func distinctApprovers(subject SubjectRef, approvals []Approval, r Requirement, at time.Time) int {
	seen := make(map[string]struct{}, len(approvals))
	for _, a := range approvals {
		if a.Decision != ApprovalGranted || !a.counts(subject, at) {
			continue
		}
		if r.Role != "" && a.Principal.Claims["role"] != r.Role {
			continue
		}
		// An approval with no principal cannot be shown to be a different
		// person from the last one, so it cannot add to the count.
		if a.Principal.ID == "" {
			continue
		}
		seen[a.Principal.ID] = struct{}{}
	}
	return len(seen)
}
