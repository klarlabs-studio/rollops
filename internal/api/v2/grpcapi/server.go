// Package grpcapi is the gRPC projection of the v2 service (§23.1).
//
// It carries request values in and response values out and decides nothing
// else. Every rule about what a call means lives in internal/api/v2, because a
// rule written here is a rule the HTTP surface and the MCP tools do not have.
// What is genuinely this package's own is narrow: which enum spells which
// domain value, how a failure code becomes a gRPC status, and the wire shape
// declared in proto/rollops/v2.
//
// No request carries an actor, and this is the reason. Who is calling is
// established by the transport and injected here, so a field for it would be a
// field a caller could fill in with somebody else's name — §5.2 wants every
// action attributable, which it is only if the attribution is not an argument.
//
// Authentication is injected for the same reason httpapi injects it: this
// package never learns what a credential looks like. It asks an Identifier who
// is calling and reports what it is told, so a deployment using mTLS and one
// reading a bearer token from metadata differ by a constructor argument rather
// than by a fork of the transport. Every call is identified, reads included —
// answering an unidentified caller's GetProject with NOT_FOUND rather than
// UNAUTHENTICATED would tell them which ids exist.
package grpcapi

import (
	"context"
	"errors"

	"google.golang.org/grpc"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	rollopsv2 "go.klarlabs.de/rollops/internal/api/v2/grpcapi/rollopsv2"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// Identifier says who is calling. It takes a context rather than a request
// because that is where gRPC puts everything a server can learn about a call —
// the incoming metadata, the peer, the TLS state. It returns an error rather
// than a bool so that an implementation which has already worked out what kind
// of refusal this is can say so with an *apierr.Error.
type Identifier interface {
	Identify(ctx context.Context) (identity.Principal, error)
}

// IdentifierFunc adapts a function to Identifier.
type IdentifierFunc func(context.Context) (identity.Principal, error)

// Identify implements Identifier.
func (f IdentifierFunc) Identify(ctx context.Context) (identity.Principal, error) { return f(ctx) }

// Config names what the server serves and who it trusts to say who is calling.
type Config struct {
	// Service answers every call. Required.
	Service *apiv2.Service

	// Identity resolves the caller. Required, and there is deliberately no
	// default: an API that authenticates when configured to would serve
	// anonymously when somebody forgot to.
	Identity Identifier
}

// Server implements rollopsv2.RollOpsServer.
type Server struct {
	rollopsv2.UnimplementedRollOpsServer

	svc *apiv2.Service
	who Identifier
}

// New returns the server, naming the first dependency it was not given.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Service == nil:
		return nil, errors.New("grpcapi: no service")
	case cfg.Identity == nil:
		return nil, errors.New("grpcapi: no identifier")
	}
	return &Server{svc: cfg.Service, who: cfg.Identity}, nil
}

// Register installs the server on a gRPC registrar, so that a composition root
// wiring this transport does not import the generated package to do it.
func (s *Server) Register(reg grpc.ServiceRegistrar) {
	rollopsv2.RegisterRollOpsServer(reg, s)
}

// caller resolves who is calling, or the refusal to say so.
//
// An Identifier that has already decided what kind of refusal this is keeps its
// answer: flattening everything to UNAUTHENTICATED would tell a caller whose
// credential is fine but whose permissions are not to go and authenticate
// again, which will not help. The same classification httpapi makes, made
// separately because neither transport should depend on the other.
func (s *Server) caller(ctx context.Context) (identity.Principal, error) {
	who, err := s.who.Identify(ctx)
	if err == nil {
		return who, nil
	}
	var already *apierr.Error
	if errors.As(err, &already) {
		return identity.Principal{}, already
	}
	return identity.Principal{}, &apierr.Error{
		Code:    apierr.Unauthorized,
		Message: "the caller was not identified",
		Err:     err,
	}
}

// reader identifies a caller whose principal the call does not otherwise need.
func (s *Server) reader(ctx context.Context) error {
	_, err := s.caller(ctx)
	return err
}

// CreateProject opens a namespace.
func (s *Server) CreateProject(
	ctx context.Context, in *rollopsv2.CreateProjectRequest,
) (*rollopsv2.Project, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	p, err := s.svc.CreateProject(ctx, apiv2.CreateProjectRequest{
		Name:           in.GetName(),
		Description:    in.GetDescription(),
		Labels:         in.GetLabels(),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoProject(p), nil
}

// GetProject returns one project.
func (s *Server) GetProject(
	ctx context.Context, in *rollopsv2.GetProjectRequest,
) (*rollopsv2.Project, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	p, err := s.svc.GetProject(ctx, apiv2.GetProjectRequest{ID: in.GetId()})
	if err != nil {
		return nil, Status(err)
	}
	return protoProject(p), nil
}

// ListProjects returns a page of projects.
func (s *Server) ListProjects(
	ctx context.Context, in *rollopsv2.ListProjectsRequest,
) (*rollopsv2.ListProjectsResponse, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	out, err := s.svc.ListProjects(ctx, apiv2.ListProjectsRequest{Page: pageOf(in.GetPage())})
	if err != nil {
		return nil, Status(err)
	}
	return &rollopsv2.ListProjectsResponse{
		Projects:      each(out.Projects, protoProject),
		NextPageToken: out.Next,
	}, nil
}

// CreateEnvironment declares a place to deploy to.
func (s *Server) CreateEnvironment(
	ctx context.Context, in *rollopsv2.CreateEnvironmentRequest,
) (*rollopsv2.Environment, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	e, err := s.svc.CreateEnvironment(ctx, apiv2.CreateEnvironmentRequest{
		ProjectID:      in.GetProjectId(),
		Name:           in.GetName(),
		Kind:           environmentKinds.name(in.GetKind()),
		Targets:        each(in.GetTargets(), apiTargetSpec),
		Policies:       each(in.GetPolicies(), apiPolicyBinding),
		Variables:      apiValues(in.GetVariables()),
		Labels:         in.GetLabels(),
		Lifecycle:      apiLifecycle(in.GetLifecycle()),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoEnvironment(e), nil
}

// GetEnvironment returns one environment.
func (s *Server) GetEnvironment(
	ctx context.Context, in *rollopsv2.GetEnvironmentRequest,
) (*rollopsv2.Environment, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	e, err := s.svc.GetEnvironment(ctx, apiv2.GetEnvironmentRequest{ID: in.GetId()})
	if err != nil {
		return nil, Status(err)
	}
	return protoEnvironment(e), nil
}

// ListEnvironments returns a page of one project's environments.
func (s *Server) ListEnvironments(
	ctx context.Context, in *rollopsv2.ListEnvironmentsRequest,
) (*rollopsv2.ListEnvironmentsResponse, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	out, err := s.svc.ListEnvironments(ctx, apiv2.ListEnvironmentsRequest{
		ProjectID: in.GetProjectId(),
		Page:      pageOf(in.GetPage()),
	})
	if err != nil {
		return nil, Status(err)
	}
	return &rollopsv2.ListEnvironmentsResponse{
		Environments:  each(out.Environments, protoEnvironment),
		NextPageToken: out.Next,
	}, nil
}

// RegisterArtifact records where a built thing is and what it must hash to.
//
// No idempotency key, and none in the proto either: the digest already
// identifies the artifact (INV-002), so a repeat of this call is the same call
// whether or not anybody labelled it as one.
func (s *Server) RegisterArtifact(
	ctx context.Context, in *rollopsv2.RegisterArtifactRequest,
) (*rollopsv2.Artifact, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	a, err := s.svc.RegisterArtifact(ctx, apiv2.RegisterArtifactRequest{
		ProjectID:  in.GetProjectId(),
		Kind:       artifactKinds.name(in.GetKind()),
		Digest:     in.GetDigest(),
		Locator:    in.GetLocator(),
		Size:       in.GetSize(),
		MediaType:  in.GetMediaType(),
		Metadata:   in.GetMetadata(),
		Provenance: apiDocument(in.GetProvenance()),
		SBOMs:      each(in.GetSboms(), apiDocument),
		Signatures: each(in.GetSignatures(), apiDocument),
		Actor:      who,
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoArtifact(a), nil
}

// GetArtifact returns one artifact.
func (s *Server) GetArtifact(
	ctx context.Context, in *rollopsv2.GetArtifactRequest,
) (*rollopsv2.Artifact, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	a, err := s.svc.GetArtifact(ctx, apiv2.GetArtifactRequest{ID: in.GetId()})
	if err != nil {
		return nil, Status(err)
	}
	return protoArtifact(a), nil
}

// ListArtifacts returns a page of one project's artifacts.
func (s *Server) ListArtifacts(
	ctx context.Context, in *rollopsv2.ListArtifactsRequest,
) (*rollopsv2.ListArtifactsResponse, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	out, err := s.svc.ListArtifacts(ctx, apiv2.ListArtifactsRequest{
		ProjectID: in.GetProjectId(),
		Page:      pageOf(in.GetPage()),
	})
	if err != nil {
		return nil, Status(err)
	}
	return &rollopsv2.ListArtifactsResponse{
		Artifacts:     each(out.Artifacts, protoArtifact),
		NextPageToken: out.Next,
	}, nil
}

// CreateRelease fixes the set of artifacts a release will deploy.
func (s *Server) CreateRelease(
	ctx context.Context, in *rollopsv2.CreateReleaseRequest,
) (*rollopsv2.Release, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	r, err := s.svc.CreateRelease(ctx, apiv2.CreateReleaseRequest{
		ProjectID:      in.GetProjectId(),
		Version:        in.GetVersion(),
		Artifacts:      each(in.GetArtifacts(), apiReleaseArtifact),
		Source:         apiSource(in.GetSource()),
		Provenance:     apiDocument(in.GetProvenance()),
		Labels:         in.GetLabels(),
		Annotations:    in.GetAnnotations(),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoRelease(r), nil
}

// GetRelease returns one release.
func (s *Server) GetRelease(
	ctx context.Context, in *rollopsv2.GetReleaseRequest,
) (*rollopsv2.Release, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	r, err := s.svc.GetRelease(ctx, apiv2.GetReleaseRequest{ID: in.GetId()})
	if err != nil {
		return nil, Status(err)
	}
	return protoRelease(r), nil
}

// ListReleases returns a page of one project's releases.
func (s *Server) ListReleases(
	ctx context.Context, in *rollopsv2.ListReleasesRequest,
) (*rollopsv2.ListReleasesResponse, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	out, err := s.svc.ListReleases(ctx, apiv2.ListReleasesRequest{
		ProjectID: in.GetProjectId(),
		Page:      pageOf(in.GetPage()),
	})
	if err != nil {
		return nil, Status(err)
	}
	return &rollopsv2.ListReleasesResponse{
		Releases:      each(out.Releases, protoRelease),
		NextPageToken: out.Next,
	}, nil
}

// CreatePlan works out what deploying a release would change and has policy
// rule on it. It touches no substrate (§8.2).
func (s *Server) CreatePlan(
	ctx context.Context, in *rollopsv2.CreatePlanRequest,
) (*rollopsv2.Plan, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	p, err := s.svc.CreatePlan(ctx, apiv2.CreatePlanRequest{
		EnvironmentID:  in.GetEnvironmentId(),
		ReleaseID:      in.GetReleaseId(),
		Strategy:       strategies.name(in.GetStrategy()),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoPlan(p), nil
}

// GetPlan returns one plan.
func (s *Server) GetPlan(
	ctx context.Context, in *rollopsv2.GetPlanRequest,
) (*rollopsv2.Plan, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	p, err := s.svc.GetPlan(ctx, apiv2.GetPlanRequest{ID: in.GetId()})
	if err != nil {
		return nil, Status(err)
	}
	return protoPlan(p), nil
}

// ApplyPlan admits a deployment for a stored plan.
func (s *Server) ApplyPlan(
	ctx context.Context, in *rollopsv2.ApplyPlanRequest,
) (*rollopsv2.Deployment, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	d, err := s.svc.ApplyPlan(ctx, apiv2.ApplyPlanRequest{
		PlanID:         in.GetPlanId(),
		Detail:         in.GetDetail(),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoDeployment(d), nil
}

// GetDeployment returns one deployment.
func (s *Server) GetDeployment(
	ctx context.Context, in *rollopsv2.GetDeploymentRequest,
) (*rollopsv2.Deployment, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	d, err := s.svc.GetDeployment(ctx, apiv2.GetDeploymentRequest{ID: in.GetId()})
	if err != nil {
		return nil, Status(err)
	}
	return protoDeployment(d), nil
}

// ListDeployments returns a page of one environment's deployments.
func (s *Server) ListDeployments(
	ctx context.Context, in *rollopsv2.ListDeploymentsRequest,
) (*rollopsv2.ListDeploymentsResponse, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	out, err := s.svc.ListDeployments(ctx, apiv2.ListDeploymentsRequest{
		EnvironmentID: in.GetEnvironmentId(),
		Page:          pageOf(in.GetPage()),
	})
	if err != nil {
		return nil, Status(err)
	}
	return &rollopsv2.ListDeploymentsResponse{
		Deployments:   each(out.Deployments, protoDeployment),
		NextPageToken: out.Next,
	}, nil
}

// ApproveDeployment records one principal's answer about a gated deployment.
func (s *Server) ApproveDeployment(
	ctx context.Context, in *rollopsv2.ApproveDeploymentRequest,
) (*rollopsv2.Deployment, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	d, err := s.svc.ApproveDeployment(ctx, apiv2.ApproveDeploymentRequest{
		DeploymentID:   in.GetDeploymentId(),
		PlanHash:       in.GetPlanHash(),
		Granted:        in.GetGranted(),
		Reason:         in.GetReason(),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoDeployment(d), nil
}

// CancelDeployment stops a deployment. Nothing already applied is undone.
func (s *Server) CancelDeployment(
	ctx context.Context, in *rollopsv2.CancelDeploymentRequest,
) (*rollopsv2.Deployment, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	d, err := s.svc.CancelDeployment(ctx, apiv2.CancelDeploymentRequest{
		DeploymentID:   in.GetDeploymentId(),
		Reason:         in.GetReason(),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoDeployment(d), nil
}

// PromoteDeployment widens a deployment that is serving.
func (s *Server) PromoteDeployment(
	ctx context.Context, in *rollopsv2.PromoteDeploymentRequest,
) (*rollopsv2.Deployment, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	d, err := s.svc.PromoteDeployment(ctx, apiv2.PromoteDeploymentRequest{
		DeploymentID:   in.GetDeploymentId(),
		Reason:         in.GetReason(),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoDeployment(d), nil
}

// RollbackDeployment returns a deployment to the release it replaced.
func (s *Server) RollbackDeployment(
	ctx context.Context, in *rollopsv2.RollbackDeploymentRequest,
) (*rollopsv2.Deployment, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	d, err := s.svc.RollbackDeployment(ctx, apiv2.RollbackDeploymentRequest{
		DeploymentID:   in.GetDeploymentId(),
		Reason:         in.GetReason(),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return protoDeployment(d), nil
}

// VerifyDeployment runs the environment's checks and returns what they found.
// A failing verdict is a successful call: the question was answered.
func (s *Server) VerifyDeployment(
	ctx context.Context, in *rollopsv2.VerifyDeploymentRequest,
) (*rollopsv2.VerifyDeploymentResponse, error) {
	who, err := s.caller(ctx)
	if err != nil {
		return nil, Status(err)
	}
	out, err := s.svc.VerifyDeployment(ctx, apiv2.VerifyDeploymentRequest{
		DeploymentID:   in.GetDeploymentId(),
		Actor:          who,
		IdempotencyKey: in.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, Status(err)
	}
	return &rollopsv2.VerifyDeploymentResponse{
		Deployment: protoDeployment(out.Deployment),
		Run:        protoVerificationRun(out.Run),
	}, nil
}

// GetVerificationRun returns what one verification concluded.
func (s *Server) GetVerificationRun(
	ctx context.Context, in *rollopsv2.GetVerificationRunRequest,
) (*rollopsv2.VerificationRun, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	run, err := s.svc.GetVerificationRun(ctx, apiv2.GetVerificationRunRequest{ID: in.GetId()})
	if err != nil {
		return nil, Status(err)
	}
	return protoVerificationRun(run), nil
}

// ListDeploymentEvents returns a page of one deployment's events, oldest first.
func (s *Server) ListDeploymentEvents(
	ctx context.Context, in *rollopsv2.ListDeploymentEventsRequest,
) (*rollopsv2.ListDeploymentEventsResponse, error) {
	if err := s.reader(ctx); err != nil {
		return nil, Status(err)
	}
	out, err := s.svc.DeploymentEvents(ctx, apiv2.DeploymentEventsRequest{
		DeploymentID: in.GetDeploymentId(),
		Page:         pageOf(in.GetPage()),
	})
	if err != nil {
		return nil, Status(err)
	}
	return &rollopsv2.ListDeploymentEventsResponse{
		Events:        each(out.Events, protoEvent),
		NextPageToken: out.Next,
	}, nil
}
