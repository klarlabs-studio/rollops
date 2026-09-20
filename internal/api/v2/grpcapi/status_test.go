package grpcapi_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/grpcapi"
)

// reasonOf digs the stable code back out of a status. It is what a caller does,
// so the tests do it the same way rather than reaching past the wire.
func reasonOf(t *testing.T, err error) string {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a status: %v", err)
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	t.Fatalf("status carries no ErrorInfo: %v", st.Proto())
	return ""
}

func TestTheStableCodeSurvivesTheLossyMapping(t *testing.T) {
	t.Parallel()

	// Several codes share a gRPC status, so the status alone cannot say which
	// failure this was. The ErrorInfo is what a caller branches on.
	for _, c := range apierr.Codes() {
		err := grpcapi.Status(&apierr.Error{Code: c, Message: "no", Err: errors.New("no")})
		if got := reasonOf(t, err); got != string(c) {
			t.Errorf("%s came back as reason %q", c, got)
		}
	}
}

func TestTheMappingIsTheOneSpec234Describes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		code apierr.Code
		want codes.Code
	}{
		{apierr.InvalidArgument, codes.InvalidArgument},
		{apierr.NotFound, codes.NotFound},
		{apierr.Conflict, codes.Aborted},
		{apierr.PlanStale, codes.FailedPrecondition},
		{apierr.PolicyDenied, codes.PermissionDenied},
		{apierr.ApprovalRequired, codes.FailedPrecondition},
		{apierr.Unauthorized, codes.Unauthenticated},
		{apierr.Forbidden, codes.PermissionDenied},
		{apierr.TargetUnavailable, codes.Unavailable},
		{apierr.CapabilityUnsupported, codes.Unimplemented},
		{apierr.VerificationFailed, codes.FailedPrecondition},
		{apierr.ExecutionFailed, codes.Internal},
		{apierr.Cancelled, codes.Canceled},
		{apierr.DeadlineExceeded, codes.DeadlineExceeded},
		{apierr.Internal, codes.Internal},
	} {
		err := grpcapi.Status(&apierr.Error{Code: tc.code, Message: "no", Err: errors.New("no")})
		if got := status.Code(err); got != tc.want {
			t.Errorf("%s mapped to %v, want %v", tc.code, got, tc.want)
		}
	}
}

func TestAnUnclassifiedFailureIsInternalAndSaysNothingElse(t *testing.T) {
	t.Parallel()

	// apierr withholds an unclassified error's own text because error text in
	// this codebase routinely carries paths, queries and connection strings
	// (INV-012). The transport must not put it back.
	err := grpcapi.Status(errors.New("dial tcp 10.0.0.1:5432: password=hunter2"))
	if got := status.Code(err); got != codes.Internal {
		t.Errorf("an unclassified failure mapped to %v, want Internal", got)
	}
	if msg := status.Convert(err).Message(); msg != "the request could not be completed" {
		t.Errorf("message %q leaks or invents detail", msg)
	}
	if got := reasonOf(t, err); got != string(apierr.Internal) {
		t.Errorf("reason %q, want INTERNAL", got)
	}
}

func TestNilIsNotAFailure(t *testing.T) {
	t.Parallel()

	if err := grpcapi.Status(nil); err != nil {
		t.Errorf("Status(nil) = %v, want nil", err)
	}
}

func TestACancelledContextKeepsItsOwnStatus(t *testing.T) {
	t.Parallel()

	// A caller that went away should read CANCELED rather than an internal
	// error, so the daemon's logs do not fill with failures nobody caused.
	if got := status.Code(grpcapi.Status(context.Canceled)); got != codes.Canceled {
		t.Errorf("a cancelled context mapped to %v, want Canceled", got)
	}
	if got := status.Code(grpcapi.Status(context.DeadlineExceeded)); got != codes.DeadlineExceeded {
		t.Errorf("an expired deadline mapped to %v, want DeadlineExceeded", got)
	}
}
