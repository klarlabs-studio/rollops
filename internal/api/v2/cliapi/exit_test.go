package cliapi_test

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/cliapi"
)

func TestNothingIsSuccess(t *testing.T) {
	t.Parallel()

	if got := cliapi.Exit(nil); got != cliapi.ExitOK {
		t.Errorf("no error exited %d", got)
	}
}

func TestTheMappingIsTheOneSpec264Describes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		code apierr.Code
		want cliapi.ExitCode
	}{
		{apierr.InvalidArgument, cliapi.ExitValidation},
		{apierr.NotFound, cliapi.ExitValidation},
		{apierr.Conflict, cliapi.ExitValidation},
		{apierr.PlanStale, cliapi.ExitValidation},
		{apierr.CapabilityUnsupported, cliapi.ExitValidation},
		{apierr.PolicyDenied, cliapi.ExitPolicy},
		{apierr.Forbidden, cliapi.ExitPolicy},
		{apierr.Unauthorized, cliapi.ExitPolicy},
		{apierr.ApprovalRequired, cliapi.ExitApprovalRequired},
		{apierr.VerificationFailed, cliapi.ExitVerificationFailed},
		{apierr.ExecutionFailed, cliapi.ExitDeploymentFailed},
		{apierr.TargetUnavailable, cliapi.ExitSystem},
		{apierr.Cancelled, cliapi.ExitSystem},
		{apierr.DeadlineExceeded, cliapi.ExitSystem},
		{apierr.Internal, cliapi.ExitSystem},
	} {
		err := &apierr.Error{Code: tc.code, Message: "no", Err: errors.New("no")}
		if got := cliapi.Exit(err); got != tc.want {
			t.Errorf("%s exited %d, want %d", tc.code, got, tc.want)
		}
	}
}

// The one distinction spec 26.4 draws that the operator acts on: a gate waiting
// for a decision is not a refusal, and a script that treated them alike would
// either abandon a deployment that only needed an approval or keep asking for
// approvals that will never lift the refusal.
func TestBeingRefusedAndNeedingAnApprovalAreDifferentExits(t *testing.T) {
	t.Parallel()

	denied := cliapi.Exit(&apierr.Error{Code: apierr.PolicyDenied, Err: errors.New("no")})
	waiting := cliapi.Exit(&apierr.Error{Code: apierr.ApprovalRequired, Err: errors.New("no")})
	if denied == waiting {
		t.Fatalf("POLICY_DENIED and APPROVAL_REQUIRED both exit %d", denied)
	}
}

// An unclassified failure is ours, not the caller's, exactly as apierr's
// INTERNAL default says — so it must not exit as a validation error and send an
// operator looking at their own command.
func TestAnUnclassifiedFailureExitsAsOurs(t *testing.T) {
	t.Parallel()

	if got := cliapi.Exit(errors.New("something nobody classified")); got != cliapi.ExitSystem {
		t.Errorf("an unclassified failure exited %d", got)
	}
}

// Exit classifies rather than requiring a caller to classify first, so that a
// command can return the error it got.
func TestARawSentinelIsClassifiedOnTheWayOut(t *testing.T) {
	t.Parallel()

	if got := cliapi.Exit(context.DeadlineExceeded); got != cliapi.ExitSystem {
		t.Errorf("a deadline exited %d", got)
	}
}

// Nothing may exit 1. A wrapper, a shell and a killed process all produce it,
// and a code we also produced could not be told apart from those.
func TestNoFailureExitsOne(t *testing.T) {
	t.Parallel()

	for _, c := range apierr.Codes() {
		if got := cliapi.Exit(&apierr.Error{Code: c, Err: errors.New("no")}); got == 1 {
			t.Errorf("%s exits 1", c)
		}
	}
}
