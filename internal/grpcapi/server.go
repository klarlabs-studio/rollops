// Package grpcapi is the daemon's typed gRPC surface over the engine — the
// transport the CLI (daemon mode) and agents use, peer to the REST surface in
// internal/api. RPCs map 1:1 to engine operations; every call is authenticated
// (bearer token in "authorization" metadata by default) via a unary interceptor
// and authorized through the same RBAC policy as every other interface. TLS or
// mTLS can be supplied with grpc.ServerOption at construction/deployment time.
package grpcapi

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"go.klarlabs.de/rollops/internal/api"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/grpcapi/rollopsv1"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/security"
)

// Server implements the generated RolloutServiceServer.
type Server struct {
	rollopsv1.UnimplementedRolloutServiceServer
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
	rollopsv1.RegisterRolloutServiceServer(gs, s)
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
func (s *Server) Plan(ctx context.Context, req *rollopsv1.PlanRequest) (*rollopsv1.PlanResponse, error) {
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
	return &rollopsv1.PlanResponse{Action: string(p.Action), Changed: p.Changed, Summary: p.Summary}, nil
}

// Apply implements the Apply RPC.
func (s *Server) Apply(ctx context.Context, req *rollopsv1.ApplyRequest) (*rollopsv1.ApplyResponse, error) {
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
	rl, err := s.eng.Apply(ctx, engine.ApplyRequest{Config: c, Initiator: id, Planned: true, Risk: engine.RiskFromConfig(c)})
	if err != nil {
		if err == engine.ErrTargetBusy {
			return nil, status.Error(codes.Aborted, err.Error())
		}
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &rollopsv1.ApplyResponse{Id: rl.ID, Phase: string(rl.Phase), Target: rl.TargetRef}, nil
}

// Status implements the Status RPC.
func (s *Server) Status(ctx context.Context, req *rollopsv1.StatusRequest) (*rollopsv1.StatusResponse, error) {
	id := identityFrom(ctx)
	if err := s.policy.Authorize(id, security.PermStatus, security.Scope{}); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	rl, err := s.eng.Status(ctx, req.GetId())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	return &rollopsv1.StatusResponse{
		Id: rl.ID, Phase: string(rl.Phase), Target: rl.TargetRef, Strategy: string(rl.Strategy),
		StepIndex: int32(rl.StepIndex), StepTotal: int32(rl.StepTotal), StepWeight: int32(rl.StepWeight),
		Note: rl.Note,
	}, nil
}

// Rollback implements the Rollback RPC.
func (s *Server) Rollback(ctx context.Context, req *rollopsv1.RollbackRequest) (*rollopsv1.RollbackResponse, error) {
	id := identityFrom(ctx)
	target := req.GetTarget()
	if target == "" {
		return nil, status.Error(codes.InvalidArgument, "target required")
	}
	if err := s.policy.Authorize(id, security.PermRollback, security.Scope{TargetRef: target}); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	rl, err := s.eng.RollbackLast(ctx, target, req.GetForce())
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &rollopsv1.RollbackResponse{Id: rl.ID, Phase: string(rl.Phase), Target: rl.TargetRef}, nil
}

// Approve implements the Approve RPC.
func (s *Server) Approve(ctx context.Context, req *rollopsv1.RolloutActionRequest) (*rollopsv1.RolloutActionResponse, error) {
	return s.rolloutAction(ctx, req.GetId(), security.PermApprove, func(id rollout.Identity) (rollout.Rollout, error) {
		return s.eng.Approve(ctx, req.GetId(), id)
	})
}

// Reject implements the Reject RPC.
func (s *Server) Reject(ctx context.Context, req *rollopsv1.RolloutActionRequest) (*rollopsv1.RolloutActionResponse, error) {
	return s.rolloutAction(ctx, req.GetId(), security.PermApprove, func(id rollout.Identity) (rollout.Rollout, error) {
		return s.eng.Reject(ctx, req.GetId(), id)
	})
}

// Promote implements the Promote RPC, gated on the post-deploy checks.
// req.Force overrides a failing gate; the bypass is audited.
func (s *Server) Promote(ctx context.Context, req *rollopsv1.RolloutActionRequest) (*rollopsv1.RolloutActionResponse, error) {
	return s.rolloutAction(ctx, req.GetId(), security.PermPromote, func(by rollout.Identity) (rollout.Rollout, error) {
		return s.eng.Promote(ctx, req.GetId(), by, req.GetForce())
	})
}

// Verify implements the Verify RPC: a dry run of the post-deploy gate that
// changes nothing. Authorized as PermPromote, not a read permission — the gates
// really run, and a configured smoke test executes a command on the daemon
// host, so this is not something a view-only caller may trigger.
func (s *Server) Verify(ctx context.Context, req *rollopsv1.RolloutActionRequest) (*rollopsv1.VerifyResponse, error) {
	actor := identityFrom(ctx)
	if req.GetId() == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	cur, err := s.eng.Status(ctx, req.GetId())
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err := s.policy.Authorize(actor, security.PermPromote, security.Scope{TargetRef: cur.TargetRef}); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	rep, err := s.eng.Verify(ctx, req.GetId())
	if err != nil {
		// Operational failure (e.g. an unreadable captured descriptor). A failing
		// GATE is not an error — it comes back in the report with ok=false.
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	gates := make([]*rollopsv1.GateResult, 0, len(rep.Gates))
	for _, g := range rep.Gates {
		gates = append(gates, &rollopsv1.GateResult{Gate: g.Gate, Status: g.Status, Detail: g.Detail})
	}
	return &rollopsv1.VerifyResponse{
		Id: rep.RolloutID, Phase: rep.Phase, Target: rep.TargetRef,
		Ok: rep.OK, Reason: rep.Reason, Gates: gates,
	}, nil
}

// rolloutAction is the shared approve/reject/promote flow: validate id, scope
// authorization to the rollout's target, run the engine op, return its outcome.
func (s *Server) rolloutAction(ctx context.Context, id string, perm security.Permission, op func(rollout.Identity) (rollout.Rollout, error)) (*rollopsv1.RolloutActionResponse, error) {
	actor := identityFrom(ctx)
	if id == "" {
		return nil, status.Error(codes.InvalidArgument, "id required")
	}
	cur, err := s.eng.Status(ctx, id)
	if err != nil {
		return nil, status.Error(codes.NotFound, err.Error())
	}
	if err := s.policy.Authorize(actor, perm, security.Scope{TargetRef: cur.TargetRef}); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	rl, err := op(actor)
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &rollopsv1.RolloutActionResponse{Id: rl.ID, Phase: string(rl.Phase), Target: rl.TargetRef, Note: rl.Note}, nil
}

// Freeze implements the Freeze RPC (emergency kill-switch).
func (s *Server) Freeze(ctx context.Context, req *rollopsv1.FreezeRequest) (*rollopsv1.FreezeResponse, error) {
	actor := identityFrom(ctx)
	if err := s.policy.Authorize(actor, security.PermFreeze, security.Scope{}); err != nil {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	active, reason, err := s.eng.Freeze(ctx, req.GetActive(), actor, req.GetReason())
	if err != nil {
		return nil, status.Error(codes.FailedPrecondition, err.Error())
	}
	return &rollopsv1.FreezeResponse{Active: active, Reason: reason}, nil
}

func scopeOf(c *config.Config) security.Scope {
	return security.Scope{Env: c.Spec.Target.Env, TargetRef: c.Spec.Target.Ref}
}
