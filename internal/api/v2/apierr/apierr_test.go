package apierr_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/page"
	"go.klarlabs.de/rollops/internal/app/deploy"
	"go.klarlabs.de/rollops/internal/app/port"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
	"go.klarlabs.de/rollops/internal/engine/desired"
	"go.klarlabs.de/rollops/internal/engine/planner"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// spec is the code set from spec 23.4, in the order the spec lists it.
var spec = []apierr.Code{
	"INVALID_ARGUMENT",
	"NOT_FOUND",
	"CONFLICT",
	"PLAN_STALE",
	"POLICY_DENIED",
	"APPROVAL_REQUIRED",
	"UNAUTHORIZED",
	"FORBIDDEN",
	"TARGET_UNAVAILABLE",
	"CAPABILITY_UNSUPPORTED",
	"VERIFICATION_FAILED",
	"EXECUTION_FAILED",
	"CANCELLED",
	"DEADLINE_EXCEEDED",
	"INTERNAL",
}

func TestTheCodeSetIsTheOneTheSpecNames(t *testing.T) {
	if got := apierr.Codes(); !slices.Equal(got, spec) {
		t.Fatalf("codes %v, want %v", got, spec)
	}
}

func TestNoErrorHasNoCode(t *testing.T) {
	if got := apierr.Of(nil); got != "" {
		t.Errorf("Of(nil) = %q, want empty", got)
	}
	if got := apierr.From(nil); got != nil {
		t.Errorf("From(nil) = %v, want nil", got)
	}
}

func TestDomainErrorsCarryTheCodeACallerCanActOn(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want apierr.Code
	}{
		{port.ErrNotFound, apierr.NotFound},
		{port.ErrAlreadyExists, apierr.Conflict},
		{port.ErrRevisionConflict, apierr.Conflict},

		{plan.ErrPlanStale, apierr.PlanStale},
		{plan.ErrPlanExpired, apierr.PlanStale},
		{plan.ErrPlanTampered, apierr.Conflict},

		{policy.ErrRequirementUnmet, apierr.ApprovalRequired},
		{policy.ErrApprovalDenied, apierr.PolicyDenied},
		{policy.ErrDecisionRefuses, apierr.PolicyDenied},
		{deploy.ErrPolicyRefused, apierr.PolicyDenied},

		{page.ErrBadCursor, apierr.InvalidArgument},
		{identity.ErrWrongKind, apierr.InvalidArgument},
		{deploy.ErrCrossProject, apierr.InvalidArgument},
		{deploy.ErrUnboundApproval, apierr.InvalidArgument},
		{deploy.ErrNoTarget, apierr.Conflict},
		{deploy.ErrEnvironmentBusy, apierr.Conflict},
		{deploy.ErrNotAwaitingApproval, apierr.Conflict},

		{planner.ErrNothingToDo, apierr.Conflict},
		{planner.ErrBlocked, apierr.Conflict},
		{desired.ErrNothingToDeploy, apierr.InvalidArgument},
		{desired.ErrForeignArtifact, apierr.InvalidArgument},

		{context.Canceled, apierr.Cancelled},
		{context.DeadlineExceeded, apierr.DeadlineExceeded},
	} {
		if got := apierr.Of(tc.err); got != tc.want {
			t.Errorf("Of(%v) = %s, want %s", tc.err, got, tc.want)
		}
	}
}

func TestATargetFailureKeepsItsKind(t *testing.T) {
	for _, tc := range []struct {
		kind targetv2.Kind
		want apierr.Code
	}{
		{targetv2.KindUnsupported, apierr.CapabilityUnsupported},
		{targetv2.KindUnavailable, apierr.TargetUnavailable},
		{targetv2.KindInvalid, apierr.InvalidArgument},
		{targetv2.KindNotFound, apierr.NotFound},
		{targetv2.KindDenied, apierr.Forbidden},
		{targetv2.KindConflict, apierr.Conflict},
		{targetv2.KindTimeout, apierr.DeadlineExceeded},
		{targetv2.KindCanceled, apierr.Cancelled},
		{targetv2.KindInternal, apierr.Internal},
	} {
		err := targetv2.Failf(tc.kind, "Apply", nil, "the substrate said no")
		if got := apierr.Of(err); got != tc.want {
			t.Errorf("a %s target failure is %s, want %s", tc.kind, got, tc.want)
		}
	}
}

func TestAWrappedErrorKeepsItsCode(t *testing.T) {
	err := fmt.Errorf("deploy: plan pln_1: %w", plan.ErrPlanStale)

	if got := apierr.Of(err); got != apierr.PlanStale {
		t.Fatalf("Of = %s, want %s", got, apierr.PlanStale)
	}
}

func TestAnUnrecognisedErrorIsInternal(t *testing.T) {
	if got := apierr.Of(errors.New("something nobody classified")); got != apierr.Internal {
		t.Fatalf("Of = %s, want %s", got, apierr.Internal)
	}
}

func TestAnInternalErrorDoesNotTellTheCallerWhatWentWrong(t *testing.T) {
	// The default is the whole point of the default: an unclassified error is
	// one nobody read for what it says, and error text in this codebase
	// routinely carries file paths, queries and connection strings (INV-012).
	cause := errors.New("open /etc/rollops/prod-kubeconfig: permission denied")

	got := apierr.From(fmt.Errorf("loading the target: %w", cause))

	if got.Code != apierr.Internal {
		t.Fatalf("code %s, want %s", got.Code, apierr.Internal)
	}
	if strings.Contains(got.Message, "prod-kubeconfig") {
		t.Errorf("the caller was told %q", got.Message)
	}
}

func TestAnInternalErrorStillCarriesItsCauseForTheLog(t *testing.T) {
	cause := errors.New("open /etc/rollops/prod-kubeconfig: permission denied")

	got := apierr.From(fmt.Errorf("loading the target: %w", cause))

	if !errors.Is(got, cause) {
		t.Fatal("the cause did not survive classification")
	}
	if !strings.Contains(got.Error(), "prod-kubeconfig") {
		t.Errorf("the log was told %q", got.Error())
	}
}

func TestAClassifiedErrorSaysWhatHappened(t *testing.T) {
	got := apierr.From(fmt.Errorf("deploy: environment env_1: %w", deploy.ErrEnvironmentBusy))

	if !strings.Contains(got.Message, "already has a deployment in flight") {
		t.Fatalf("message %q does not explain the refusal", got.Message)
	}
}

func TestClassifyingDoesNotBreakTheErrorChain(t *testing.T) {
	got := apierr.From(fmt.Errorf("deploy: %w", plan.ErrPlanStale))

	if !errors.Is(got, plan.ErrPlanStale) {
		t.Fatal("errors.Is no longer finds the sentinel")
	}
}

func TestClassifyingTwiceChangesNothing(t *testing.T) {
	once := apierr.From(deploy.ErrEnvironmentBusy)

	if twice := apierr.From(once); twice != once {
		t.Fatalf("From reclassified its own result: %+v, want %+v", twice, once)
	}
}

func TestEveryCodeHasAnHTTPStatus(t *testing.T) {
	for _, c := range apierr.Codes() {
		if got := c.HTTP(); got < 400 || got > 599 {
			t.Errorf("%s maps to HTTP %d", c, got)
		}
	}
}

func TestTheHTTPStatusSaysWhoseFaultItIs(t *testing.T) {
	for _, tc := range []struct {
		code apierr.Code
		want int
	}{
		{apierr.InvalidArgument, http.StatusBadRequest},
		{apierr.NotFound, http.StatusNotFound},
		{apierr.Conflict, http.StatusConflict},
		{apierr.PlanStale, http.StatusConflict},
		{apierr.ApprovalRequired, http.StatusConflict},
		{apierr.PolicyDenied, http.StatusForbidden},
		{apierr.Forbidden, http.StatusForbidden},
		{apierr.Unauthorized, http.StatusUnauthorized},
		{apierr.TargetUnavailable, http.StatusServiceUnavailable},
		{apierr.CapabilityUnsupported, http.StatusNotImplemented},
		{apierr.VerificationFailed, http.StatusUnprocessableEntity},
		{apierr.ExecutionFailed, http.StatusInternalServerError},
		{apierr.DeadlineExceeded, http.StatusGatewayTimeout},
		{apierr.Internal, http.StatusInternalServerError},
	} {
		if got := tc.code.HTTP(); got != tc.want {
			t.Errorf("%s is HTTP %d, want %d", tc.code, got, tc.want)
		}
	}
}

func TestAnUnknownCodeIsTreatedAsInternal(t *testing.T) {
	if got := apierr.Code("MADE_UP").HTTP(); got != http.StatusInternalServerError {
		t.Fatalf("an unknown code is HTTP %d, want 500", got)
	}
}

func TestAPolicyRefusalIsNotAnApprovalRequest(t *testing.T) {
	// One says the gate is waiting and the caller should go get approvals; the
	// other says policy refused and no number of approvals will change it
	// (13). A transport that collapsed them would make a refusal look like a
	// pending one, and the caller would wait forever.
	waiting := apierr.Of(fmt.Errorf("%w: two of four", policy.ErrRequirementUnmet))
	refused := apierr.Of(fmt.Errorf("%w: production is frozen", policy.ErrDecisionRefuses))

	if waiting == refused {
		t.Fatalf("both are %s", waiting)
	}
}
