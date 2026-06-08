// Package grpcapi is the daemon's typed gRPC surface over the engine — the
// transport the CLI (daemon mode) and agents use, peer to the REST surface in
// internal/api. RPCs map 1:1 to engine operations; every call is authenticated
// (bearer token in "authorization" metadata) via a unary interceptor and
// authorized through the same RBAC policy as every other interface.
package grpcapi

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"go.klarlabs.de/rolloffs/internal/api"
	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/grpcapi/rolloffsv1"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/security"
)

// Server implements the generated RolloutServiceServer.
type Server struct {
	rolloffsv1.UnimplementedRolloutServiceServer
	eng    *engine.Engine
	auth   api.Authenticator
	policy *security.Policy
}

// New builds the gRPC server.
func New(eng *engine.Engine, auth api.Authenticator, policy *security.Policy) *Server {
	return &Server{eng: eng, auth: auth, policy: policy}
}

// Register attaches the service and the auth interceptor is added at server
// construction (see NewGRPCServer).
func (s *Server) Register(gs grpc.ServiceRegistrar) {
	rolloffsv1.RegisterRolloutServiceServer(gs, s)
}

// NewGRPCServer builds a *grpc.Server with the auth interceptor installed and
// the service registered.
func NewGRPCServer(s *Server, opts ...grpc.ServerOption) *grpc.Server {
	opts = append(opts, grpc.UnaryInterceptor(s.authInterceptor))
	gs := grpc.NewServer(opts...)
	s.Register(gs)
	return gs
}

type ctxKey int

const idKey ctxKey = 0

// authInterceptor resolves the bearer token from metadata to an identity, or
// rejects with Unauthenticated. No anonymous calls.
func (s *Server) authInterceptor(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	token := bearerFromMD(ctx)
	id, ok := s.auth.Identify(token)
	if token == "" || !ok {
		return nil, status.Error(codes.Unauthenticated, "unauthorized")
	}
	return handler(context.WithValue(ctx, idKey, id), req)
}

func identityFrom(ctx context.Context) rollout.Identity {
	if id, ok := ctx.Value(idKey).(rollout.Identity); ok {
		return id
	}
	return rollout.Identity{}
}

func bearerFromMD(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	vals := md.Get("authorization")
	if len(vals) == 0 {
		return ""
	}
	return strings.TrimPrefix(vals[0], "Bearer ")
}

// Plan implements the Plan RPC.
func (s *Server) Plan(ctx context.Context, req *rolloffsv1.PlanRequest) (*rolloffsv1.PlanResponse, error) {
	id := identityFrom(ctx)
	c, err := config.Load([]byte(req.GetConfig()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.policy.Authorize(id, security.PermPlan, scopeOf(c)); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	p, err := s.eng.Plan(ctx, c)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &rolloffsv1.PlanResponse{Action: string(p.Action), Changed: p.Changed, Summary: p.Summary}, nil
}

// Apply implements the Apply RPC.
func (s *Server) Apply(ctx context.Context, req *rolloffsv1.ApplyRequest) (*rolloffsv1.ApplyResponse, error) {
	id := identityFrom(ctx)
	c, err := config.Load([]byte(req.GetConfig()))
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.policy.Authorize(id, security.PermApply, scopeOf(c)); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	if _, err := s.eng.Plan(ctx, c); err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rl, err := s.eng.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: id, Planned: true})
	if err != nil {
		if err == engine.ErrTargetBusy {
			return nil, status.Error(codes.Aborted, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &rolloffsv1.ApplyResponse{Id: rl.ID, Phase: string(rl.Phase), Target: rl.TargetRef}, nil
}

// Status implements the Status RPC.
func (s *Server) Status(ctx context.Context, req *rolloffsv1.StatusRequest) (*rolloffsv1.StatusResponse, error) {
	id := identityFrom(ctx)
	if err := s.policy.Authorize(id, security.PermStatus, security.Scope{}); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	rl, err := s.eng.Status(ctx, req.GetId())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &rolloffsv1.StatusResponse{Id: rl.ID, Phase: string(rl.Phase), Target: rl.TargetRef, Strategy: string(rl.Strategy)}, nil
}

func scopeOf(c *config.Config) security.Scope {
	return security.Scope{TargetRef: c.Spec.Target.Ref}
}
