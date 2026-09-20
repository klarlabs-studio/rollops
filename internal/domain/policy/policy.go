// Package policy holds the record of an authorization decision and the risk
// assessment that fed it.
//
// Risk is an input to policy rather than a parallel authorization system
// (spec 12.4). There is one decision to read when asking why an apply was
// refused, not two that can disagree.
//
// This package defines the records, not the engine that produces them. A plan
// carries a decision so that what was approved is what is applied, which is why
// these types know how to encode themselves canonically but know nothing about
// CEL, configuration or evaluation.
package policy

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"go.klarlabs.de/rollops/internal/domain/canonical"
)

var (
	// ErrUnexplainedRisk reports raised risk with nothing to point at. An
	// opaque score must never be the sole authorization mechanism (spec 12.4).
	ErrUnexplainedRisk = errors.New("policy: risk is raised but unexplained")

	// ErrUnexplainedDecision reports a refusal that does not say why, which
	// leaves an operator with nothing to act on.
	ErrUnexplainedDecision = errors.New("policy: decision refuses without a reason")
)

// RiskLevel is the coarse band a risk score falls into. The band is what policy
// is written against; the score is the detail behind it.
type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

// riskLevels is the whole set, least severe first — the order rank imposes,
// stated once so that publishing the vocabulary and ordering it cannot drift.
var riskLevels = []RiskLevel{RiskLow, RiskMedium, RiskHigh, RiskCritical}

// RiskLevels returns every band, least severe first. A transport that has to
// name the whole vocabulary asks here rather than restating it, and the slice
// is rebuilt on each call so a caller cannot edit the set through it.
func RiskLevels() []RiskLevel { return slices.Clone(riskLevels) }

// rank orders the levels by their position in riskLevels. An unrecognised level
// ranks above every declared one so that a typo in configuration fails closed
// rather than reading as harmless.
func (l RiskLevel) rank() int {
	if i := slices.Index(riskLevels, l); i >= 0 {
		return i
	}
	return len(riskLevels)
}

// Below reports whether l is less severe than other.
func (l RiskLevel) Below(other RiskLevel) bool { return l.rank() < other.rank() }

func (l RiskLevel) String() string { return string(l) }

func (l RiskLevel) valid() bool {
	switch l {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	}
	return false
}

// RiskFactor is one explainable contribution to an assessment. The code is what
// policy matches on; the message is for the person reading the plan.
type RiskFactor struct {
	Code    string
	Message string
}

// RiskAssessment is the explainable estimate of how dangerous a change is.
type RiskAssessment struct {
	Level   RiskLevel
	Score   float64
	Factors []RiskFactor
}

// Validate reports whether the assessment is usable as a policy input.
func (r RiskAssessment) Validate() error {
	if !r.Level.valid() {
		return fmt.Errorf("policy: unknown risk level %q", r.Level)
	}
	if r.Score < 0 || r.Score > 1 {
		return fmt.Errorf("policy: risk score %v is not between 0 and 1", r.Score)
	}
	for i, f := range r.Factors {
		if strings.TrimSpace(f.Code) == "" {
			return fmt.Errorf("policy: risk factor %d has no code", i)
		}
	}
	if r.Level != RiskLow && len(r.Factors) == 0 {
		return fmt.Errorf("%w: level %s", ErrUnexplainedRisk, r.Level)
	}
	return nil
}

// Encode writes the assessment into a canonical encoding. Factors are written
// in the order given: an assessment lists its reasoning, and reordering it is a
// different explanation of the same score.
func (r RiskAssessment) Encode(w *canonical.Writer) {
	w.String(string(r.Level))
	w.Float(r.Score)
	canonical.Each(w, r.Factors, func(w *canonical.Writer, f RiskFactor) {
		w.String(f.Code)
		w.String(f.Message)
	})
}

// RequirementType names a condition that must hold before an apply may proceed.
type RequirementType string

const (
	RequireApproval       RequirementType = "approval"
	RequireSignedArtifact RequirementType = "signed_artifact"
	RequireProvenance     RequirementType = "provenance"
	RequireStagingSuccess RequirementType = "staging_success"
	RequireTimeWindow     RequirementType = "time_window"
	RequireChangeTicket   RequirementType = "change_ticket"
)

// requirementTypes is the whole set, in the order the constants declare it.
var requirementTypes = []RequirementType{
	RequireApproval, RequireSignedArtifact, RequireProvenance,
	RequireStagingSuccess, RequireTimeWindow, RequireChangeTicket,
}

// RequirementTypes returns every condition policy can attach to a decision, in
// a stable order. A transport that has to name the whole vocabulary asks here
// rather than restating it, and the slice is rebuilt on each call so a caller
// cannot edit the set through it.
func RequirementTypes() []RequirementType { return slices.Clone(requirementTypes) }

func (t RequirementType) valid() bool { return slices.Contains(requirementTypes, t) }

// Requirement is a condition attached to a decision. Role and Count qualify an
// approval requirement; Detail carries the qualifier for the rest.
type Requirement struct {
	Type   RequirementType
	Role   string
	Count  int
	Detail string
}

// Validate reports whether the requirement states a condition that can be met.
func (r Requirement) Validate() error {
	if !r.Type.valid() {
		return fmt.Errorf("policy: unknown requirement type %q", r.Type)
	}
	// A requirement for zero approvals is satisfied by doing nothing. It reads
	// as a gate in a plan without being one, which is worse than no gate.
	if r.Type == RequireApproval && r.Count < 1 {
		return fmt.Errorf("policy: approval requirement demands %d approvals", r.Count)
	}
	return nil
}

// Reason explains a decision. The code is stable for matching; the message is
// for the person reading it.
type Reason struct {
	Code    string
	Message string
}

// Decision is the outcome of evaluating policy against a change.
type Decision struct {
	Allowed      bool
	Requirements []Requirement
	Reasons      []Reason
	Risk         RiskAssessment
}

// Validate reports whether the decision is a complete, reviewable record.
func (d Decision) Validate() error {
	if err := d.Risk.Validate(); err != nil {
		return err
	}
	for i, r := range d.Requirements {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("policy: requirement %d: %w", i, err)
		}
	}
	for i, r := range d.Reasons {
		if strings.TrimSpace(r.Code) == "" {
			return fmt.Errorf("policy: reason %d has no code", i)
		}
	}
	if !d.Allowed && len(d.Reasons) == 0 {
		return ErrUnexplainedDecision
	}
	return nil
}

// Encode writes the decision into a canonical encoding so that a plan hash
// covers what policy concluded. Requirements are what stands between a plan and
// an apply, so weakening one has to change the hash.
func (d Decision) Encode(w *canonical.Writer) {
	w.Bool(d.Allowed)
	canonical.Each(w, d.Requirements, func(w *canonical.Writer, r Requirement) {
		w.String(string(r.Type))
		w.String(r.Role)
		w.Int(int64(r.Count))
		w.String(r.Detail)
	})
	canonical.Each(w, d.Reasons, func(w *canonical.Writer, r Reason) {
		w.String(r.Code)
		w.String(r.Message)
	})
	d.Risk.Encode(w)
}
