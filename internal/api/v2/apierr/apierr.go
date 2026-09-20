// Package apierr classifies a failure into the stable code every v2 transport
// reports it by (spec 23.4).
//
// The codes are contract. gRPC, HTTP/JSON, MCP and the CLI all answer with the
// same one for the same failure, which is what lets a caller branch on the code
// rather than on a status number that three transports spell differently.
//
// Classification is deliberately a whitelist with an INTERNAL default. An error
// that nobody has read and decided about is not a caller's fault by default: it
// is ours, and saying INVALID_ARGUMENT would send an operator looking at their
// own request. The default also governs what a caller is told, because error
// text in this codebase routinely carries paths, queries and connection strings
// (INV-012) — a classified error's message is one we wrote on purpose, and an
// unclassified one's is withheld and logged instead.
//
// The HTTP mapping lives here. The gRPC mapping lives with the gRPC server, so
// that the CLI and MCP do not acquire a gRPC dependency to name an error; each
// transport's mapping is held exhaustive by a test over Codes().
package apierr

import (
	"context"
	"errors"
	"net/http"

	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/app/port"
	appproject "go.klarlabs.de/rollops/internal/app/project"
	"go.klarlabs.de/rollops/internal/app/release"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/engine/desired"
	"go.klarlabs.de/rollops/internal/engine/planner"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// Code is the stable name of a failure. It is a string rather than an integer
// because it crosses a wire into logs, dashboards and scripts, where a number
// nobody can read is a number nobody checks.
type Code string

const (
	// InvalidArgument means the request is wrong and will fail the same way if
	// repeated unchanged.
	InvalidArgument Code = "INVALID_ARGUMENT"

	// NotFound means the thing the request names is not there.
	NotFound Code = "NOT_FOUND"

	// Conflict means the request is well-formed but disagrees with the state of
	// what it names. Repeating it after the state moves may succeed.
	Conflict Code = "CONFLICT"

	// PlanStale means the plan is no longer the plan that was approved, whether
	// because it expired or because the world moved under it. The remedy is
	// always the same: plan again.
	PlanStale Code = "PLAN_STALE"

	// PolicyDenied means policy refused. Approvals do not change it; the gate
	// waiting for them is ApprovalRequired.
	PolicyDenied Code = "POLICY_DENIED"

	// ApprovalRequired means the gate is waiting rather than refusing. It is a
	// separate code from PolicyDenied because the caller's next move differs:
	// go and get approvals, versus stop.
	ApprovalRequired Code = "APPROVAL_REQUIRED"

	// Unauthorized means the caller was not identified.
	Unauthorized Code = "UNAUTHORIZED"

	// Forbidden means the caller was identified and is not allowed.
	Forbidden Code = "FORBIDDEN"

	// TargetUnavailable means the substrate could not be reached. Nothing is
	// wrong with the request; retrying later is the expected response.
	TargetUnavailable Code = "TARGET_UNAVAILABLE"

	// CapabilityUnsupported means the target does not have a capability the
	// operation needs (INV-014). The call never happened.
	CapabilityUnsupported Code = "CAPABILITY_UNSUPPORTED"

	// VerificationFailed means the deployment landed and did not pass its
	// checks.
	VerificationFailed Code = "VERIFICATION_FAILED"

	// ExecutionFailed means the apply itself failed. Whether anything landed is
	// the deployment's timeline to answer, not this code's.
	ExecutionFailed Code = "EXECUTION_FAILED"

	// Cancelled means the caller went away or the operation was cancelled.
	Cancelled Code = "CANCELLED"

	// DeadlineExceeded means the deadline passed.
	DeadlineExceeded Code = "DEADLINE_EXCEEDED"

	// Internal is everything else, and it is the default. See the package
	// comment for why that is not laziness.
	Internal Code = "INTERNAL"
)

// Codes returns the whole set, in the order spec 23.4 lists it. Transports
// range over it to prove their own mapping is exhaustive, so a code added here
// breaks the transport that has not decided what to do with it — which is the
// point.
func Codes() []Code {
	return []Code{
		InvalidArgument, NotFound, Conflict, PlanStale, PolicyDenied,
		ApprovalRequired, Unauthorized, Forbidden, TargetUnavailable,
		CapabilityUnsupported, VerificationFailed, ExecutionFailed,
		Cancelled, DeadlineExceeded, Internal,
	}
}

// httpStatus maps a code to the status a JSON transport answers with.
//
// Several codes share a status, which is expected: HTTP has fewer distinctions
// than spec 23.4 does, and the code is what survives. PLAN_STALE, CONFLICT and
// APPROVAL_REQUIRED are all 409 because all three mean the request disagrees
// with current state and may succeed once that state moves; the body says which.
var httpStatus = map[Code]int{
	InvalidArgument:       http.StatusBadRequest,
	NotFound:              http.StatusNotFound,
	Conflict:              http.StatusConflict,
	PlanStale:             http.StatusConflict,
	ApprovalRequired:      http.StatusConflict,
	PolicyDenied:          http.StatusForbidden,
	Forbidden:             http.StatusForbidden,
	Unauthorized:          http.StatusUnauthorized,
	TargetUnavailable:     http.StatusServiceUnavailable,
	CapabilityUnsupported: http.StatusNotImplemented,
	VerificationFailed:    http.StatusUnprocessableEntity,
	ExecutionFailed:       http.StatusInternalServerError,
	// 499 is nginx's, not the RFC's, and it is what gRPC-gateway already emits
	// for a cancelled call. Inventing a 4xx of our own would be worse.
	Cancelled:        499,
	DeadlineExceeded: http.StatusGatewayTimeout,
	Internal:         http.StatusInternalServerError,
}

// HTTP returns the status a JSON transport answers with. An unknown code is 500
// on the same reasoning as the INTERNAL default: nobody decided about it.
func (c Code) HTTP() int {
	if s, ok := httpStatus[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

func (c Code) String() string { return string(c) }

// Error is a failure with a code and a message safe to hand a caller.
type Error struct {
	Code Code

	// Message is what the caller is told. For a classified failure it is the
	// error's own text, which is wording we chose. For an unclassified one it
	// is generic, and the detail is in Err.
	Message string

	// Err is the original, for the log and for errors.Is. It never reaches a
	// caller by itself.
	Err error
}

func (e *Error) Error() string { return e.Err.Error() }

func (e *Error) Unwrap() error { return e.Err }

// From classifies err, or returns nil for nil. An *Error is returned unchanged:
// classifying twice would re-derive the same code and throw away a message a
// handler may already have narrowed.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var already *Error
	if errors.As(err, &already) {
		return already
	}
	code := Of(err)
	msg := err.Error()
	if code == Internal {
		msg = "the request could not be completed"
	}
	return &Error{Code: code, Message: msg, Err: err}
}

// Of reports the code for err without building an Error. Order matters where
// two sentinels could both match: the more specific answer is checked first, so
// a stale plan is PLAN_STALE rather than the CONFLICT it also is.
func Of(err error) Code {
	switch {
	case err == nil:
		return ""

	case errors.Is(err, context.Canceled):
		return Cancelled
	case errors.Is(err, context.DeadlineExceeded):
		return DeadlineExceeded

	case errors.Is(err, port.ErrNotFound):
		return NotFound

	// Expired and stale are one answer because the remedy is one action. A plan
	// that no longer hashes to its recorded hash is not stale — nothing moved
	// on, the record disagrees with itself — so it is a plain conflict, and an
	// operator who sees that code rather than PLAN_STALE knows to go looking.
	case errors.Is(err, plan.ErrPlanStale), errors.Is(err, plan.ErrPlanExpired):
		return PlanStale

	case errors.Is(err, policy.ErrRequirementUnmet):
		return ApprovalRequired
	case errors.Is(err, policy.ErrApprovalDenied),
		errors.Is(err, policy.ErrDecisionRefuses),
		errors.Is(err, deploy.ErrPolicyRefused):
		return PolicyDenied

	case errors.Is(err, port.ErrAlreadyExists),
		errors.Is(err, port.ErrRevisionConflict),
		errors.Is(err, plan.ErrPlanTampered),
		errors.Is(err, deploy.ErrEnvironmentBusy),
		errors.Is(err, deploy.ErrNotAwaitingApproval),
		// A deployment past the point of stopping is not a malformed request:
		// the same command a moment earlier would have worked.
		errors.Is(err, deploy.ErrNotCancellable),
		// An environment with no target, one already at the release, and one
		// whose target reported a blocker are all the same shape of answer:
		// the request is fine and the world is not ready for it.
		errors.Is(err, deploy.ErrNoTarget),
		errors.Is(err, planner.ErrNothingToDo),
		errors.Is(err, planner.ErrBlocked):
		return Conflict

	case errors.Is(err, page.ErrBadCursor),
		// An id of the wrong kind is well-formed enough to look up. Treating it
		// as missing would send the caller hunting for a resource that was never
		// addressable at that name.
		errors.Is(err, identity.ErrWrongKind),
		errors.Is(err, deploy.ErrCrossProject),
		errors.Is(err, deploy.ErrUnboundApproval),
		// A refusal that says nothing about why is a request that will fail the
		// same way however often it is sent, and leaves whoever it stops with
		// nothing to act on. INTERNAL would invite the retry instead.
		errors.Is(err, policy.ErrUnexplainedDecision),
		errors.Is(err, deploy.ErrUnexplainedCancellation),
		errors.Is(err, desired.ErrNothingToDeploy),
		errors.Is(err, desired.ErrForeignArtifact),
		// The same isolation breach as desired.ErrForeignArtifact, refused when
		// the release is created rather than when it is planned.
		errors.Is(err, release.ErrForeignArtifact),
		// The domain says what is wrong with the record; the app layer says it
		// came off the command, so a caller is told to fix their request rather
		// than to report an outage.
		errors.Is(err, release.ErrRejected),
		// The same reasoning one aggregate earlier: a project name that is not
		// an alias is a typo, and a typo answered with INTERNAL invites the
		// retry that will fail identically.
		errors.Is(err, appproject.ErrRejected):
		return InvalidArgument
	}

	if k := targetv2.KindOf(err); k != targetv2.KindInternal {
		return fromTargetKind(k)
	}
	return Internal
}

// fromTargetKind carries a target's own typed failure through without losing
// the distinction it drew (ADR-0006). A target that said "I do not have this
// capability" must not arrive as a generic failure: the caller's response to
// that is to carry on without the capability.
func fromTargetKind(k targetv2.Kind) Code {
	switch k {
	case targetv2.KindUnsupported:
		return CapabilityUnsupported
	case targetv2.KindUnavailable:
		return TargetUnavailable
	case targetv2.KindInvalid:
		return InvalidArgument
	case targetv2.KindNotFound:
		return NotFound
	case targetv2.KindDenied:
		return Forbidden
	case targetv2.KindConflict:
		return Conflict
	case targetv2.KindTimeout:
		return DeadlineExceeded
	case targetv2.KindCanceled:
		return Cancelled
	default:
		return Internal
	}
}
