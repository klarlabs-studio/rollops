package apiv2

import (
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/policy"
)

// Action names one call a caller may make next. The vocabulary is the API's own
// operations rather than the statuses underneath them, because what an agent
// needs is the name of the thing to invoke.
const (
	ActionApprove         = "approve"
	ActionVerify          = "verify"
	ActionPromote         = "promote"
	ActionRollback        = "rollback"
	ActionCancel          = "cancel"
	ActionApplyPlan       = "apply_plan"
	ActionRequestApproval = "request_approval"
)

// NextActions lists what may be done to this deployment now, in a stable order.
//
// Every entry is derived from the same transition table the engine enforces, so
// the list cannot offer an action the next call would refuse — the one failure
// mode worth designing out, because an agent that is told it may promote and is
// then refused has no way to tell a stale answer from a bug. It lives here
// rather than in a transport because §23.1 settles semantics once: a rule
// written in the MCP tools is a rule gRPC and HTTP callers do not have.
//
// A terminal or unrecognised status yields nil rather than an empty slice, so
// "nothing to do" reads the same whether the deployment finished or the status
// is one this build does not know.
func (d Deployment) NextActions() []string {
	s := deployment.Status(d.Status)
	if !s.Valid() {
		return nil
	}
	var out []string
	// Approving is the one action that is not a transition of its own: it
	// answers the gate, and the answer may be no, so it is offered by the
	// status that is waiting rather than by an edge.
	if s == deployment.StatusAwaitingApproval {
		out = append(out, ActionApprove)
	}
	for _, c := range []struct {
		action string
		to     deployment.Status
	}{
		{ActionVerify, deployment.StatusVerifying},
		{ActionPromote, deployment.StatusPromoting},
		{ActionRollback, deployment.StatusRollingBack},
		{ActionCancel, deployment.StatusCancelled},
	} {
		if s.CanTransitionTo(c.to) {
			out = append(out, c.action)
		}
	}
	return out
}

// ApprovalRequired reports whether the gate is waiting rather than refusing —
// the same distinction apierr draws between APPROVAL_REQUIRED and
// POLICY_DENIED, and for the same reason: the caller's next move differs.
func (p Plan) ApprovalRequired() bool {
	if p.Policy.Allowed {
		return false
	}
	for _, r := range p.Policy.Requirements {
		if r.Type == string(policy.RequireApproval) {
			return true
		}
	}
	return false
}

// NextActions lists what may be done with this plan now.
//
// Policy has already decided, so this reports the decision rather than making a
// second one: a plan the decision allows may be applied, and one held up by a
// requirement offers the call that would satisfy it. A plan refused for a
// reason no approval can lift offers nothing, because naming an action that
// cannot succeed is worse than naming none.
func (p Plan) NextActions() []string {
	switch {
	case p.Policy.Allowed:
		return []string{ActionApplyPlan}
	case p.ApprovalRequired():
		return []string{ActionRequestApproval}
	default:
		return nil
	}
}
