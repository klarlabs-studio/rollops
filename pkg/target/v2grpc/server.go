// Package v2grpc carries the pkg/target/v2 contract over the
// rollops.target.v2.Target service: NewServer presents a target to a host,
// NewClient presents a host's view of a remote one (ADR-0006).
//
// Both halves exist so that the same contract value behaves the same whether
// the target is linked in or is a subprocess. That is what the capability
// model rests on — a host cannot ask a subprocess which Go interfaces it
// implements, so the answer has to come from Capabilities either way, and the
// client below satisfies every optional subinterface precisely so that
// asserting one tells a caller nothing.
package v2grpc

import (
	"context"

	pb "go.klarlabs.de/rollops/pkg/target/rollopstargetv2"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// Server serves one target. It is the plugin side of the wire.
type Server struct {
	pb.UnimplementedTargetServer
	inner targetv2.Target
}

// NewServer wraps t for serving. The optional capabilities are resolved by
// asserting t — the one place that is still correct, because this code and t
// are in the same process. A target that does not implement one answers
// unsupported rather than going missing, which is what lets the host treat
// every method as present.
func NewServer(t targetv2.Target) *Server { return &Server{inner: t} }

func (s *Server) GetCapabilities(ctx context.Context, _ *pb.GetCapabilitiesRequest) (*pb.GetCapabilitiesResponse, error) {
	caps, err := s.inner.Capabilities(ctx)
	if err != nil {
		return nil, toStatus(err)
	}
	names := caps.Names()
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, string(n))
	}
	return &pb.GetCapabilitiesResponse{Capabilities: out}, nil
}

func (s *Server) Inspect(ctx context.Context, _ *pb.InspectRequest) (*pb.InspectResponse, error) {
	state, err := s.inner.Inspect(ctx, targetv2.InspectRequest{})
	if err != nil {
		return nil, toStatus(err)
	}
	res := make([]*pb.Resource, 0, len(state.Resources))
	for _, r := range state.Resources {
		res = append(res, &pb.Resource{
			Kind:      r.Kind,
			Name:      r.Name,
			Namespace: r.Namespace,
			Status:    r.Status,
			Parent:    r.Parent,
		})
	}
	return &pb.InspectResponse{
		Fingerprint: state.Fingerprint,
		Resources:   res,
		Meta:        state.Meta,
	}, nil
}

func (s *Server) Plan(ctx context.Context, req *pb.PlanRequest) (*pb.PlanResponse, error) {
	res, err := s.inner.Plan(ctx, targetv2.PlanRequest{Desired: desiredFromWire(req.GetDesired())})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.PlanResponse{
		Changes:          res.Changes,
		Diff:             res.Diff,
		Rendered:         res.Rendered,
		Blockers:         res.Blockers,
		RenderedChecksum: res.RenderedChecksum,
	}, nil
}

func (s *Server) Apply(ctx context.Context, req *pb.ApplyRequest) (*pb.ApplyResponse, error) {
	res, err := s.inner.Apply(ctx, targetv2.ApplyRequest{
		Desired:        desiredFromWire(req.GetDesired()),
		IdempotencyKey: req.GetIdempotencyKey(),
	})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ApplyResponse{Changed: res.Changed, Detail: res.Detail, Handle: res.Handle}, nil
}

func (s *Server) Observe(ctx context.Context, req *pb.ObserveRequest) (*pb.ObserveResponse, error) {
	obs, err := s.inner.Observe(ctx, targetv2.ObserveRequest{Handle: req.GetHandle()})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.ObserveResponse{
		Fingerprint: obs.Fingerprint,
		Health: &pb.HealthStatus{
			State:  healthToWire(obs.Health.State),
			Reason: obs.Health.Reason,
		},
		Meta: obs.Meta,
	}, nil
}

func (s *Server) Rollback(ctx context.Context, req *pb.RollbackRequest) (*pb.RollbackResponse, error) {
	res, err := s.inner.Rollback(ctx, targetv2.RollbackRequest{Handle: req.GetHandle()})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.RollbackResponse{Changed: res.Changed, Detail: res.Detail}, nil
}

func (s *Server) Promote(ctx context.Context, req *pb.PromoteRequest) (*pb.PromoteResponse, error) {
	impl, ok := s.inner.(targetv2.Promoter)
	if !ok {
		return nil, toStatus(targetv2.Unsupported("Promote", targetv2.CapabilityProgressiveDelivery))
	}
	res, err := impl.Promote(ctx, targetv2.PromoteRequest{Handle: req.GetHandle()})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.PromoteResponse{Detail: res.Detail}, nil
}

func (s *Server) DetectDrift(ctx context.Context, req *pb.DetectDriftRequest) (*pb.DetectDriftResponse, error) {
	impl, ok := s.inner.(targetv2.Drifter)
	if !ok {
		return nil, toStatus(targetv2.Unsupported("DetectDrift", targetv2.CapabilityDriftDetection))
	}
	res, err := impl.DetectDrift(ctx, targetv2.DriftRequest{Desired: desiredFromWire(req.GetDesired())})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.DetectDriftResponse{Drifted: res.Drifted, Detail: res.Detail}, nil
}

func (s *Server) Prune(ctx context.Context, _ *pb.PruneRequest) (*pb.PruneResponse, error) {
	impl, ok := s.inner.(targetv2.Pruner)
	if !ok {
		return nil, toStatus(targetv2.Unsupported("Prune", targetv2.CapabilityPrune))
	}
	res, err := impl.Prune(ctx, targetv2.PruneRequest{})
	if err != nil {
		return nil, toStatus(err)
	}
	return &pb.PruneResponse{Removed: int32(res.Removed)}, nil //nolint:gosec // a prune count does not exceed int32
}

func desiredToWire(d targetv2.DesiredState) *pb.DesiredState {
	return &pb.DesiredState{
		Kind:     d.Kind,
		Spec:     d.Spec,
		Checksum: d.Checksum,
		Labels:   d.Labels,
		Rendered: d.Rendered,
	}
}

func desiredFromWire(d *pb.DesiredState) targetv2.DesiredState {
	return targetv2.DesiredState{
		Kind:     d.GetKind(),
		Spec:     d.GetSpec(),
		Checksum: d.GetChecksum(),
		Labels:   d.GetLabels(),
		Rendered: d.GetRendered(),
	}
}

func healthToWire(s targetv2.HealthState) pb.HealthState {
	switch s {
	case targetv2.HealthHealthy:
		return pb.HealthState_HEALTH_STATE_HEALTHY
	case targetv2.HealthDegraded:
		return pb.HealthState_HEALTH_STATE_DEGRADED
	case targetv2.HealthUnhealthy:
		return pb.HealthState_HEALTH_STATE_UNHEALTHY
	case targetv2.HealthUnknown:
		return pb.HealthState_HEALTH_STATE_UNSPECIFIED
	}
	return pb.HealthState_HEALTH_STATE_UNSPECIFIED
}

func healthFromWire(s pb.HealthState) targetv2.HealthState {
	switch s {
	case pb.HealthState_HEALTH_STATE_HEALTHY:
		return targetv2.HealthHealthy
	case pb.HealthState_HEALTH_STATE_DEGRADED:
		return targetv2.HealthDegraded
	case pb.HealthState_HEALTH_STATE_UNHEALTHY:
		return targetv2.HealthUnhealthy
	case pb.HealthState_HEALTH_STATE_UNSPECIFIED:
		return targetv2.HealthUnknown
	}
	return targetv2.HealthUnknown
}
