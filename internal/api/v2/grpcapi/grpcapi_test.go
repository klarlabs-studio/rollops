package grpcapi_test

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/grpcapi"
	rollopsv2 "go.klarlabs.de/rollops/internal/api/v2/grpcapi/rollopsv2"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

func anyone() identity.Principal {
	return identity.Principal{ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada"}
}

// nobody stands in for a missing or unreadable credential.
func nobody() grpcapi.IdentifierFunc {
	return func(context.Context) (identity.Principal, error) {
		return identity.Principal{}, errors.New("no credential")
	}
}

func everyone() grpcapi.IdentifierFunc {
	return func(context.Context) (identity.Principal, error) { return anyone(), nil }
}

func TestAServiceAndAnIdentifierAreBothRequired(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  grpcapi.Config
	}{
		{"no service", grpcapi.Config{Identity: everyone()}},
		{"no identifier", grpcapi.Config{Service: &apiv2.Service{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := grpcapi.New(tc.cfg); err == nil {
				t.Fatal("New accepted a config it cannot serve")
			}
		})
	}
}

// reason is the stable code a caller reads out of the status details.
//
// It is the contract, not the gRPC code: §23.4 draws distinctions gRPC has no
// code for, so a caller telling PLAN_STALE from APPROVAL_REQUIRED is reading
// this and not the status.
func reason(t *testing.T, err error) string {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("not a gRPC status: %v", err)
	}
	for _, d := range st.Details() {
		if info, is := d.(*errdetails.ErrorInfo); is {
			return info.GetReason()
		}
	}
	t.Fatalf("the status carried no ErrorInfo: %v", err)
	return ""
}

// Every RPC is refused to an unidentified caller, reads included, and refused
// before anything is read: answering a read with NOT_FOUND would tell somebody
// who has not said who they are which ids exist. The table is the whole
// service, because the one read that forgets to ask is the one worth finding —
// the service behind it is empty, so any call that got past the check would
// answer NOT_FOUND rather than UNAUTHENTICATED.
func TestEveryRPCRefusesAnUnidentifiedCaller(t *testing.T) {
	t.Parallel()
	client := dial(t, &apiv2.Service{}, nobody())
	ctx := context.Background()

	calls := map[string]func() error{
		"CreateProject": func() error {
			_, err := client.CreateProject(ctx, &rollopsv2.CreateProjectRequest{})
			return err
		},
		"GetProject": func() error {
			_, err := client.GetProject(ctx, &rollopsv2.GetProjectRequest{})
			return err
		},
		"ListProjects": func() error {
			_, err := client.ListProjects(ctx, &rollopsv2.ListProjectsRequest{})
			return err
		},
		"CreateEnvironment": func() error {
			_, err := client.CreateEnvironment(ctx, &rollopsv2.CreateEnvironmentRequest{})
			return err
		},
		"GetEnvironment": func() error {
			_, err := client.GetEnvironment(ctx, &rollopsv2.GetEnvironmentRequest{})
			return err
		},
		"ListEnvironments": func() error {
			_, err := client.ListEnvironments(ctx, &rollopsv2.ListEnvironmentsRequest{})
			return err
		},
		"RegisterArtifact": func() error {
			_, err := client.RegisterArtifact(ctx, &rollopsv2.RegisterArtifactRequest{})
			return err
		},
		"GetArtifact": func() error {
			_, err := client.GetArtifact(ctx, &rollopsv2.GetArtifactRequest{})
			return err
		},
		"ListArtifacts": func() error {
			_, err := client.ListArtifacts(ctx, &rollopsv2.ListArtifactsRequest{})
			return err
		},
		"CreateRelease": func() error {
			_, err := client.CreateRelease(ctx, &rollopsv2.CreateReleaseRequest{})
			return err
		},
		"GetRelease": func() error {
			_, err := client.GetRelease(ctx, &rollopsv2.GetReleaseRequest{})
			return err
		},
		"ListReleases": func() error {
			_, err := client.ListReleases(ctx, &rollopsv2.ListReleasesRequest{})
			return err
		},
		"CreatePlan": func() error {
			_, err := client.CreatePlan(ctx, &rollopsv2.CreatePlanRequest{})
			return err
		},
		"GetPlan": func() error {
			_, err := client.GetPlan(ctx, &rollopsv2.GetPlanRequest{})
			return err
		},
		"ApplyPlan": func() error {
			_, err := client.ApplyPlan(ctx, &rollopsv2.ApplyPlanRequest{})
			return err
		},
		"GetDeployment": func() error {
			_, err := client.GetDeployment(ctx, &rollopsv2.GetDeploymentRequest{})
			return err
		},
		"ListDeployments": func() error {
			_, err := client.ListDeployments(ctx, &rollopsv2.ListDeploymentsRequest{})
			return err
		},
		"ApproveDeployment": func() error {
			_, err := client.ApproveDeployment(ctx, &rollopsv2.ApproveDeploymentRequest{})
			return err
		},
		"CancelDeployment": func() error {
			_, err := client.CancelDeployment(ctx, &rollopsv2.CancelDeploymentRequest{})
			return err
		},
		"PromoteDeployment": func() error {
			_, err := client.PromoteDeployment(ctx, &rollopsv2.PromoteDeploymentRequest{})
			return err
		},
		"RollbackDeployment": func() error {
			_, err := client.RollbackDeployment(ctx, &rollopsv2.RollbackDeploymentRequest{})
			return err
		},
		"VerifyDeployment": func() error {
			_, err := client.VerifyDeployment(ctx, &rollopsv2.VerifyDeploymentRequest{})
			return err
		},
		"GetVerificationRun": func() error {
			_, err := client.GetVerificationRun(ctx, &rollopsv2.GetVerificationRunRequest{})
			return err
		},
		"ListDeploymentEvents": func() error {
			_, err := client.ListDeploymentEvents(ctx, &rollopsv2.ListDeploymentEventsRequest{})
			return err
		},
	}

	// The service declares this many methods, so a new RPC that nobody added
	// here fails rather than going quietly unguarded.
	if want := len(rollopsv2.RollOps_ServiceDesc.Methods); len(calls) != want {
		t.Fatalf("the table covers %d RPCs, the service declares %d", len(calls), want)
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := call()
			if got := status.Code(err); got != codes.Unauthenticated {
				t.Errorf("code = %s, want UNAUTHENTICATED", got)
			}
			if got := reason(t, err); got != string(apierr.Unauthorized) {
				t.Errorf("reason = %q, want UNAUTHORIZED", got)
			}
		})
	}
}

// An Identifier that has already decided what kind of refusal this is keeps its
// answer. Flattening everything to UNAUTHENTICATED would tell a caller whose
// credential is fine but whose permissions are not to go and authenticate
// again, which will not help.
func TestAnIdentifierMayRefuseWithItsOwnCode(t *testing.T) {
	t.Parallel()
	forbidden := grpcapi.IdentifierFunc(func(context.Context) (identity.Principal, error) {
		return identity.Principal{}, &apierr.Error{
			Code:    apierr.Forbidden,
			Message: "not permitted",
			Err:     errors.New("not permitted"),
		}
	})
	client := dial(t, &apiv2.Service{}, forbidden)

	_, err := client.GetDeployment(context.Background(), &rollopsv2.GetDeploymentRequest{Id: "dpl_1"})

	if got := status.Code(err); got != codes.PermissionDenied {
		t.Errorf("code = %s, want PERMISSION_DENIED", got)
	}
	if got := reason(t, err); got != string(apierr.Forbidden) {
		t.Errorf("reason = %q, want FORBIDDEN", got)
	}
}

// A malformed id is the caller's mistake, and it is the service that says so —
// this transport parses nothing.
func TestAMalformedIDIsTheCallersMistake(t *testing.T) {
	t.Parallel()
	e := served(t)

	_, err := e.client.GetDeployment(context.Background(), &rollopsv2.GetDeploymentRequest{
		Id: "not-a-deployment-id",
	})

	if got := status.Code(err); got != codes.InvalidArgument {
		t.Errorf("code = %s, want INVALID_ARGUMENT", got)
	}
	if got := reason(t, err); got != string(apierr.InvalidArgument) {
		t.Errorf("reason = %q, want INVALID_ARGUMENT", got)
	}
}
