// Package gate is the built-in policy engine: it decides what has to happen
// before a plan may be applied.
//
// It answers spec 12.2's contract with the requirements the system can actually
// discharge today — approvals. The domain knows about signed artifacts,
// provenance and time windows too, but nothing yet satisfies those, and a
// requirement nothing can discharge is not a gate: it is an environment that
// can never be deployed to again. They are attached when there is something
// that answers them.
//
// Risk reaches a decision through this engine rather than beside it (spec
// 12.4). The score cannot refuse anything on its own; what it can do is cross a
// band an operator configured, which attaches an approval requirement like any
// other rule. There is one decision to read when asking why an apply was
// gated, and the assessment that fed it is inside that decision.
package gate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/policy/risk"
)

// ErrUnusableRules reports configuration that could not gate anything. It is
// returned from New rather than discovered on the first deployment of the day,
// because a rule that silently does nothing is indistinguishable from a rule
// that was never written.
var ErrUnusableRules = errors.New("gate: rules cannot be enforced as written")

// Reason codes are stable so a surface can tell what gated a deployment
// without matching on prose.
const (
	// ReasonRiskBand reports an approval attached because the assessment
	// reached a band the operator configured.
	ReasonRiskBand = "risk_band"

	// ReasonPolicy reports an enforced policy binding that contributed.
	ReasonPolicy = "policy"

	// ReasonAdvisory reports a binding in warn mode: the operator asked to be
	// told rather than stopped, so it explains and requires nothing.
	ReasonAdvisory = "policy_advisory"

	// ReasonUnknownPolicy reports a binding to a policy that is not loaded.
	ReasonUnknownPolicy = "unknown_policy"
)

// Approval is a demand for people to answer. Role is the claim an approver must
// hold, or empty for anyone who may approve at all.
type Approval struct {
	Role  string
	Count int
}

// Rules is one named, reusable set of conditions.
type Rules struct {
	Approvals []Approval

	// ApproveAtOrAbove is the risk band at which a change needs a human even
	// when nothing else about it does. Empty means risk alone never gates:
	// an unset band must not read as "gate on everything", because that is
	// what an operator who never considered the field would get.
	ApproveAtOrAbove policy.RiskLevel
}

// Config is what the engine enforces.
type Config struct {
	// Floor is in force everywhere, whatever an environment binds. It exists
	// so that a production environment somebody forgot to bind a policy to is
	// still held to something.
	Floor Rules

	// Policies are the rule sets an environment's bindings reference by name.
	Policies map[string]Rules
}

// Engine evaluates Config against a request. It is immutable once built and
// safe to share: one is constructed at startup and read by every plan.
type Engine struct {
	floor    Rules
	policies map[string]Rules
}

// New returns an engine, or reports the rule that could not gate anything.
func New(cfg Config) (*Engine, error) {
	if err := cfg.Floor.validate("the floor"); err != nil {
		return nil, err
	}
	// Sorted so that a configuration with two bad rules names the same one
	// every time it is loaded.
	for _, name := range slices.Sorted(maps.Keys(cfg.Policies)) {
		if err := cfg.Policies[name].validate(fmt.Sprintf("policy %q", name)); err != nil {
			return nil, err
		}
	}
	e := &Engine{floor: cfg.Floor.clone(), policies: make(map[string]Rules, len(cfg.Policies))}
	for name, r := range cfg.Policies {
		e.policies[name] = r.clone()
	}
	return e, nil
}

func (r Rules) clone() Rules {
	r.Approvals = slices.Clone(r.Approvals)
	return r
}

func (r Rules) validate(where string) error {
	for _, a := range r.Approvals {
		// The domain refuses this too, but by then it is a plan that failed to
		// build rather than a line somebody can go and fix.
		if a.Count < 1 {
			return fmt.Errorf("%w: %s asks for %d approvals", ErrUnusableRules, where, a.Count)
		}
	}
	switch r.ApproveAtOrAbove {
	case "", policy.RiskLow, policy.RiskMedium, policy.RiskHigh, policy.RiskCritical:
		return nil
	default:
		// A misspelled band ranks above critical, so it would never be reached
		// and the rule would fail open without ever saying so.
		return fmt.Errorf("%w: %s gates at risk band %q, which is not a band",
			ErrUnusableRules, where, r.ApproveAtOrAbove)
	}
}

// demand is one role's outstanding approval count and the rule that set it.
type demand struct {
	count  int
	detail string
}

// Evaluate rules on a proposal.
//
// An unresolvable binding refuses rather than being skipped: an environment
// referencing a policy that is not loaded has been configured with a gate
// nobody can evaluate, and proceeding would apply the change that policy
// existed to hold back. That is true of an advisory binding as well — the
// operator asked for an opinion the engine cannot produce.
func (e *Engine) Evaluate(_ context.Context, req deploy.PolicyRequest) (policy.Decision, error) {
	d := policy.Decision{Allowed: true, Risk: risk.Assess(risk.Signals{
		Environment: req.Environment.Kind,
		Strategy:    req.Strategy,
		Operations:  req.Operations,
	})}

	wanted := make(map[string]demand)
	// Several rules can ask for the same people. Two of them wanting one
	// approval want one person, not two: summing would invent a four-eyes rule
	// nobody wrote, so the strictest demand wins rather than the total.
	enforce := func(r Rules, detail string) {
		add := func(a Approval, why string) {
			if cur, ok := wanted[a.Role]; !ok || a.Count > cur.count {
				wanted[a.Role] = demand{count: a.Count, detail: why}
			}
		}
		for _, a := range r.Approvals {
			add(a, detail)
		}
		if r.ApproveAtOrAbove == "" || d.Risk.Level.Below(r.ApproveAtOrAbove) {
			return
		}
		why := fmt.Sprintf("risk is %s, at or above %s", d.Risk.Level, r.ApproveAtOrAbove)
		add(Approval{Count: 1}, why)
		d.Reasons = append(d.Reasons, policy.Reason{
			Code: ReasonRiskBand, Message: fmt.Sprintf("%s (%s)", why, detail)})
	}

	enforce(e.floor, "required of every deployment")

	for _, b := range req.Environment.Policies {
		rules, ok := e.policies[b.Ref]
		if !ok {
			d.Allowed = false
			d.Reasons = append(d.Reasons, policy.Reason{
				Code: ReasonUnknownPolicy,
				Message: fmt.Sprintf("environment %q binds %q to policy %q, which is not loaded",
					req.Environment.Name, b.Name, b.Ref),
			})
			continue
		}
		if b.Mode == environment.PolicyWarn {
			d.Reasons = append(d.Reasons, policy.Reason{
				Code:    ReasonAdvisory,
				Message: fmt.Sprintf("policy %q is advisory here and requires nothing", b.Name),
			})
			continue
		}
		detail := fmt.Sprintf("required by policy %q", b.Name)
		enforce(rules, detail)
		d.Reasons = append(d.Reasons, policy.Reason{Code: ReasonPolicy, Message: detail})
	}

	// Sorted by role because the plan hash covers the requirements in the order
	// they are listed. Emitting them in map order would give two plans that
	// mean the same thing two different hashes, and an approval bound to one of
	// them would not discharge the other.
	for _, role := range slices.Sorted(maps.Keys(wanted)) {
		w := wanted[role]
		d.Requirements = append(d.Requirements, policy.Requirement{
			Type: policy.RequireApproval, Role: role, Count: w.count, Detail: w.detail,
		})
	}

	// The decision is the record everything downstream reads, so it is checked
	// here rather than left to fail when a plan is built from it.
	if err := d.Validate(); err != nil {
		return policy.Decision{}, fmt.Errorf("gate: %w", err)
	}
	return d, nil
}
