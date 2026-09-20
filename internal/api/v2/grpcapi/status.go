// Failures, rendered as gRPC statuses.
//
// The mapping lives here rather than in apierr on purpose: apierr is read by
// the CLI and by MCP, and neither should acquire a gRPC dependency in order to
// name an error. Each transport owns its own translation and proves it
// exhaustive against apierr.Codes().
package grpcapi

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
)

// errorDomain scopes the reason in an ErrorInfo. Google's convention is a
// domain a reason is unique within, so that a caller aggregating errors from
// several services cannot confuse our NOT_FOUND with somebody else's.
const errorDomain = "v2.rollops.klarlabs.de"

// grpcStatus maps a stable code to the status a gRPC transport answers with.
//
// The mapping is lossier than HTTP's, not merely different: gRPC has one
// FAILED_PRECONDITION where spec 23.4 has three distinct reasons a request
// disagrees with current state, and one PERMISSION_DENIED where it has two
// kinds of refusal. That is why the code travels in the status details as well
// — a caller that branched on the gRPC code alone could not tell "re-plan"
// from "get an approval", which are different things to go and do.
var grpcStatus = map[apierr.Code]codes.Code{
	apierr.InvalidArgument: codes.InvalidArgument,
	apierr.NotFound:        codes.NotFound,

	// ABORTED is gRPC's concurrency conflict and carries the advice to retry at
	// a higher level, which is exactly what CONFLICT means. PLAN_STALE and
	// APPROVAL_REQUIRED are FAILED_PRECONDITION instead because neither is
	// fixed by retrying: one needs a new plan and the other a decision.
	apierr.Conflict:           codes.Aborted,
	apierr.PlanStale:          codes.FailedPrecondition,
	apierr.ApprovalRequired:   codes.FailedPrecondition,
	apierr.VerificationFailed: codes.FailedPrecondition,

	// UNAUTHENTICATED is "we do not know who you are"; PERMISSION_DENIED is "we
	// do, and no". POLICY_DENIED joins FORBIDDEN because from the wire they are
	// the same answer; the detail says which decided it.
	apierr.Unauthorized: codes.Unauthenticated,
	apierr.Forbidden:    codes.PermissionDenied,
	apierr.PolicyDenied: codes.PermissionDenied,

	apierr.TargetUnavailable:     codes.Unavailable,
	apierr.CapabilityUnsupported: codes.Unimplemented,

	apierr.ExecutionFailed:  codes.Internal,
	apierr.Cancelled:        codes.Canceled,
	apierr.DeadlineExceeded: codes.DeadlineExceeded,
	apierr.Internal:         codes.Internal,
}

// Status renders err as the status a caller receives, or nil for nil.
//
// What reaches the caller is apierr's message, never the underlying error's:
// an unclassified failure's text routinely carries paths, queries and
// connection strings (INV-012), so it is withheld here and logged instead.
func Status(err error) error {
	classified := apierr.From(err)
	if classified == nil {
		return nil
	}
	code, known := grpcStatus[classified.Code]
	if !known {
		// Same reasoning as apierr's INTERNAL default: a code nobody decided
		// about is ours, not the caller's.
		code = codes.Internal
	}
	st := status.New(code, classified.Message)
	withReason, attachErr := st.WithDetails(&errdetails.ErrorInfo{
		Reason: string(classified.Code),
		Domain: errorDomain,
	})
	if attachErr != nil {
		// WithDetails only fails if the detail cannot be marshalled, which for
		// a two-string message it cannot. Answering with the bare status is
		// still a correct answer, so this never costs a caller the failure.
		return st.Err()
	}
	return withReason.Err()
}
