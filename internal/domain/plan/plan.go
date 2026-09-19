// Package plan models a deployment plan: what would change, why it is allowed,
// and how to undo it.
//
// Planning is separated from applying so that a change can be reviewed,
// approved and reasoned about before anything moves. That separation is only
// worth something if the plan that is applied is the plan that was approved,
// which is why a plan carries a hash over everything apply depends on, expires,
// and refuses to apply against a world that has moved on.
//
// The package knows nothing about Kubernetes, Helm or any other target
// (INV-007). An operation names a target binding by the name the environment
// gave it and describes its effect as a portable diff.
package plan

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/canonical"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

var (
	// ErrPlanTampered reports a plan whose contents no longer match its hash.
	// A plan is stored, and storage is the boundary an edit arrives through.
	ErrPlanTampered = errors.New("plan: contents do not match the hash")

	// ErrPlanExpired reports a plan that is too old to trust.
	ErrPlanExpired = errors.New("plan: expired")

	// ErrPlanStale reports a plan computed against a revision of the world that
	// has since moved (spec 4.9).
	ErrPlanStale = errors.New("plan: base revision has moved")

	// ErrDuplicateOperation reports two operations claiming one identifier,
	// which would make a dependency ambiguous.
	ErrDuplicateOperation = errors.New("plan: duplicate operation id")

	// ErrUnknownDependency reports a dependency on an operation the plan does
	// not contain, which could never be satisfied.
	ErrUnknownDependency = errors.New("plan: dependency names an unknown operation")

	// ErrCyclicDependency reports a dependency cycle, which has no valid
	// execution order.
	ErrCyclicDependency = errors.New("plan: dependency cycle")
)

// OperationID identifies an operation within one plan. It is plan-local, not a
// global identifier: operations have no existence outside the plan that lists
// them (spec 43).
type OperationID string

// OperationKind names what an operation does to its target.
type OperationKind string

const (
	OperationApply    OperationKind = "apply"
	OperationRollback OperationKind = "rollback"
)

func (k OperationKind) valid() bool {
	switch k {
	case OperationApply, OperationRollback:
		return true
	}
	return false
}

// Change is one field-level difference an operation would make. Path is in the
// target's own vocabulary; From and To are rendered values.
//
// Sensitive marks a value that must not be stored or displayed (INV-011). The
// path stays visible — knowing that a database URL changed is the point of a
// diff, and only the value is the secret.
type Change struct {
	Path      string
	From      string
	To        string
	Sensitive bool
}

// Diff is the portable description of what an operation would change. It is
// what makes a plan reviewable without parsing a provider-specific blob.
type Diff struct {
	Changes []Change
}

// PlannedOperation is one step of a plan. Target names a binding declared by
// the environment; the plan never carries provider configuration itself.
type PlannedOperation struct {
	ID           OperationID
	Target       string
	Kind         OperationKind
	Summary      string
	Diff         Diff
	Dependencies []OperationID
	Reversible   bool
}

// RollbackPlan records where a failed deployment would return to. It is
// optional: not every change has a defined way back, and claiming one that does
// not exist is worse than admitting none.
type RollbackPlan struct {
	FromRelease identity.ReleaseID
	ToRelease   identity.ReleaseID
	Operations  []PlannedOperation
	Automatic   bool
}

func (r RollbackPlan) isZero() bool {
	return r.FromRelease == "" && r.ToRelease == "" &&
		len(r.Operations) == 0 && !r.Automatic
}

// DeploymentPlan is what would happen if a release were deployed to an
// environment, decided before anything moves.
type DeploymentPlan struct {
	ID            identity.PlanID
	ProjectID     identity.ProjectID
	EnvironmentID identity.EnvironmentID
	ReleaseID     identity.ReleaseID

	// BaseRevision is the environment revision the plan was computed against.
	// Apply compares it with the live revision rather than silently re-planning.
	BaseRevision identity.Revision

	// Strategy is how the operations are to be rolled out. It is part of the
	// plan, and therefore of the hash, because a change reviewed as a canary
	// and applied by replacing everything at once is not the change that was
	// reviewed — the operations are identical and the blast radius is not.
	Strategy deployment.Strategy

	Operations []PlannedOperation

	// Policy is the single authorization record. Risk lives inside it rather
	// than beside it, so there is one answer to read rather than two that can
	// disagree (spec 12.4).
	Policy policy.Decision

	Rollback RollbackPlan

	CreatedBy identity.Principal
	CreatedAt time.Time
	ExpiresAt time.Time

	// Hash covers every field above. It is excluded from its own computation.
	Hash digest.Digest
}

// New stamps identity, attribution, time and a hash onto d.
//
// The author's claims are redacted before hashing: a plan is persisted and
// rendered wherever a release is explained, so a credential that reached it
// would be impossible to recall (INV-011).
func New(
	g identity.Generator,
	c identity.Clock,
	by identity.Principal,
	lifetime time.Duration,
	d DeploymentPlan,
) (DeploymentPlan, error) {
	// A plan with no lifetime never goes stale by time, which defeats the point
	// of planning separately from applying.
	if lifetime <= 0 {
		return DeploymentPlan{}, fmt.Errorf("plan: lifetime %s is not positive", lifetime)
	}
	id, err := identity.NewPlanID(g)
	if err != nil {
		return DeploymentPlan{}, err
	}
	d.ID = id
	d.CreatedBy = by.Redacted()
	d.CreatedAt = c.Now()
	d.ExpiresAt = d.CreatedAt.Add(lifetime)
	d.Hash = digest.Digest{}
	if err := d.Validate(); err != nil {
		return DeploymentPlan{}, err
	}
	d.Hash = d.computeHash()
	return d, nil
}

// Validate reports whether the plan is a complete, executable record.
func (p DeploymentPlan) Validate() error {
	for _, f := range []struct {
		name, value string
	}{
		{"id", string(p.ID)},
		{"project id", string(p.ProjectID)},
		{"environment id", string(p.EnvironmentID)},
		{"release id", string(p.ReleaseID)},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("plan: no %s", f.name)
		}
	}
	if !p.Strategy.Valid() {
		return fmt.Errorf("plan: unknown strategy %q", p.Strategy)
	}
	if len(p.Operations) == 0 {
		return errors.New("plan: no operations; there would be nothing to apply")
	}
	if err := validateOperations(p.Operations); err != nil {
		return err
	}
	if err := p.Policy.Validate(); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if !p.Rollback.isZero() {
		// A rollback onto the release being deployed is not a rollback.
		if p.Rollback.ToRelease == p.Rollback.FromRelease {
			return fmt.Errorf("plan: rollback leads back to %q", p.Rollback.ToRelease)
		}
		if err := validateOperations(p.Rollback.Operations); err != nil {
			return fmt.Errorf("plan: rollback: %w", err)
		}
	}
	if err := p.CreatedBy.Validate(); err != nil {
		return fmt.Errorf("plan: %w", err)
	}
	if p.CreatedAt.IsZero() {
		return errors.New("plan: no creation time")
	}
	if !p.ExpiresAt.After(p.CreatedAt) {
		return errors.New("plan: expiry is not after creation")
	}
	return nil
}

func validateOperations(ops []PlannedOperation) error {
	seen := make(map[OperationID]struct{}, len(ops))
	for i, o := range ops {
		if strings.TrimSpace(string(o.ID)) == "" {
			return fmt.Errorf("plan: operation %d has no id", i)
		}
		if strings.TrimSpace(o.Target) == "" {
			return fmt.Errorf("plan: operation %q has no target", o.ID)
		}
		if !o.Kind.valid() {
			return fmt.Errorf("plan: operation %q has unknown kind %q", o.ID, o.Kind)
		}
		// Section 8.3 asks that a plan be understandable without parsing an
		// opaque provider blob, which a summary is what makes true.
		if strings.TrimSpace(o.Summary) == "" {
			return fmt.Errorf("plan: operation %q has no summary", o.ID)
		}
		if _, dup := seen[o.ID]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateOperation, o.ID)
		}
		seen[o.ID] = struct{}{}
	}
	for _, o := range ops {
		for _, dep := range o.Dependencies {
			if _, ok := seen[dep]; !ok {
				return fmt.Errorf("%w: %q depends on %q", ErrUnknownDependency, o.ID, dep)
			}
		}
	}
	return checkAcyclic(ops)
}

// checkAcyclic refuses a cycle at planning time rather than leaving it to be
// discovered halfway through an apply.
func checkAcyclic(ops []PlannedOperation) error {
	deps := make(map[OperationID][]OperationID, len(ops))
	for _, o := range ops {
		deps[o.ID] = o.Dependencies
	}
	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	state := make(map[OperationID]int, len(ops))
	var visit func(OperationID) error
	visit = func(id OperationID) error {
		switch state[id] {
		case onStack:
			return fmt.Errorf("%w: reached %q again", ErrCyclicDependency, id)
		case done:
			return nil
		}
		state[id] = onStack
		for _, dep := range deps[id] {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = done
		return nil
	}
	for _, o := range ops {
		if err := visit(o.ID); err != nil {
			return err
		}
	}
	return nil
}

// VerifyHash reports whether the plan still matches the hash it was created
// with.
func (p DeploymentPlan) VerifyHash() error {
	if p.Hash.IsZero() {
		return fmt.Errorf("%w: plan carries no hash", ErrPlanTampered)
	}
	if got := p.computeHash(); got != p.Hash {
		return fmt.Errorf("%w: recomputed %s, stored %s", ErrPlanTampered, got, p.Hash)
	}
	return nil
}

// CheckApplicable reports whether the plan may still be applied against the
// world as it is now. It answers a structural question only: whether the plan's
// policy requirements have been met is the caller's decision, because the plan
// cannot see the approvals.
//
// Tampering is reported ahead of expiry. An expired plan is routine; a tampered
// one needs investigating, and reporting the routine failure first would hide
// it.
func (p DeploymentPlan) CheckApplicable(now time.Time, current identity.Revision) error {
	if err := p.VerifyHash(); err != nil {
		return err
	}
	// Expiry is a deadline, not a window that includes its own end.
	if !now.Before(p.ExpiresAt) {
		return fmt.Errorf("%w: at %s, expired %s", ErrPlanExpired, now, p.ExpiresAt)
	}
	if p.BaseRevision != current {
		return fmt.Errorf("%w: planned at %d, now %d", ErrPlanStale, p.BaseRevision, current)
	}
	return nil
}

// Redacted returns a copy safe to store and render, with the value of every
// change marked sensitive removed (INV-011). The receiver is untouched, and the
// copy still verifies its hash: sensitive values were never inside it.
func (p DeploymentPlan) Redacted() DeploymentPlan {
	p.Operations = redactOperations(p.Operations)
	p.Rollback.Operations = redactOperations(p.Rollback.Operations)
	return p
}

func redactOperations(ops []PlannedOperation) []PlannedOperation {
	if ops == nil {
		return nil
	}
	out := make([]PlannedOperation, len(ops))
	copy(out, ops)
	for i, o := range out {
		if o.Diff.Changes == nil {
			continue
		}
		changes := make([]Change, len(o.Diff.Changes))
		copy(changes, o.Diff.Changes)
		for j, c := range changes {
			if c.Sensitive {
				changes[j].From = ""
				changes[j].To = ""
			}
		}
		out[i].Diff.Changes = changes
	}
	return out
}

// computeHash derives the plan's content identity from everything apply relies
// on. A field left out here can be edited in storage after approval without
// disturbing the hash, so the rule is that apply must not read anything this
// does not cover.
func (p DeploymentPlan) computeHash() digest.Digest {
	var w canonical.Writer
	w.String(string(p.ID))
	w.String(string(p.ProjectID))
	w.String(string(p.EnvironmentID))
	w.String(string(p.ReleaseID))
	w.Uint(uint64(p.BaseRevision))
	w.String(string(p.Strategy))
	encodeOperations(&w, p.Operations)
	w.Nested(p.Policy.Encode)
	w.Nested(func(w *canonical.Writer) {
		w.String(string(p.Rollback.FromRelease))
		w.String(string(p.Rollback.ToRelease))
		w.Bool(p.Rollback.Automatic)
		encodeOperations(w, p.Rollback.Operations)
	})
	w.Nested(func(w *canonical.Writer) {
		w.String(p.CreatedBy.ID)
		w.String(string(p.CreatedBy.Type))
		w.String(p.CreatedBy.DisplayName)
		w.StringMap(p.CreatedBy.Claims)
	})
	w.Time(p.CreatedAt)
	w.Time(p.ExpiresAt)
	return w.Sum()
}

func encodeOperations(w *canonical.Writer, ops []PlannedOperation) {
	canonical.Each(w, ops, func(w *canonical.Writer, o PlannedOperation) {
		w.String(string(o.ID))
		w.String(o.Target)
		w.String(string(o.Kind))
		w.String(o.Summary)
		w.Bool(o.Reversible)
		canonical.Each(w, o.Dependencies, func(w *canonical.Writer, d OperationID) {
			w.String(string(d))
		})
		canonical.Each(w, o.Diff.Changes, func(w *canonical.Writer, c Change) {
			w.String(c.Path)
			w.Bool(c.Sensitive)
			// The value of a sensitive change is deliberately outside the hash.
			// It is never persisted, so there is nothing stored for an edit to
			// reach; hashing it would instead make Redacted produce a plan that
			// fails its own verification.
			if !c.Sensitive {
				w.String(c.From)
				w.String(c.To)
			}
		})
	})
}
