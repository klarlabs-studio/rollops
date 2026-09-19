package v2grpc

import (
	"context"

	pb "go.klarlabs.de/rollops/pkg/target/rollopstargetv2"
	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

// Client is the host's view of a remote target. It implements every optional
// subinterface unconditionally, which is the point rather than an oversight:
// asserting one must tell a caller nothing, so that the only way to learn what
// the target can do is to ask.
type Client struct {
	remote pb.TargetClient
	meta   targetv2.Metadata
}

var (
	_ targetv2.Target   = (*Client)(nil)
	_ targetv2.Promoter = (*Client)(nil)
	_ targetv2.Drifter  = (*Client)(nil)
	_ targetv2.Pruner   = (*Client)(nil)
)

// NewClient wraps a connected target service. The metadata is supplied by the
// host because a plugin does not know how it was named in configuration — it
// knows how to act on its substrate and nothing about the deployment that
// reached for it.
func NewClient(remote pb.TargetClient, meta targetv2.Metadata) *Client {
	return &Client{remote: remote, meta: meta}
}

// Metadata identifies the target. It is local: no call, nothing that can fail.
func (c *Client) Metadata() targetv2.Metadata { return c.meta }

// Capabilities asks the target what it can do. Names this host does not know
// are ignored rather than refused, so a plugin may gain a capability in a patch
// release without breaking every host older than it.
func (c *Client) Capabilities(ctx context.Context) (targetv2.Capabilities, error) {
	res, err := c.remote.GetCapabilities(ctx, &pb.GetCapabilitiesRequest{})
	if err != nil {
		return targetv2.Capabilities{}, fromStatus("Capabilities", err)
	}
	caps, _ := targetv2.ParseCapabilities(res.GetCapabilities())
	return caps, nil
}

func (c *Client) Inspect(ctx context.Context, _ targetv2.InspectRequest) (targetv2.ObservedState, error) {
	res, err := c.remote.Inspect(ctx, &pb.InspectRequest{})
	if err != nil {
		return targetv2.ObservedState{}, fromStatus("Inspect", err)
	}
	state := targetv2.ObservedState{Fingerprint: res.GetFingerprint(), Meta: res.GetMeta()}
	for _, r := range res.GetResources() {
		state.Resources = append(state.Resources, targetv2.Resource{
			Kind:      r.GetKind(),
			Name:      r.GetName(),
			Namespace: r.GetNamespace(),
			Status:    r.GetStatus(),
			Parent:    r.GetParent(),
		})
	}
	return state, nil
}

func (c *Client) Plan(ctx context.Context, req targetv2.PlanRequest) (targetv2.PlanResult, error) {
	res, err := c.remote.Plan(ctx, &pb.PlanRequest{Desired: desiredToWire(req.Desired)})
	if err != nil {
		return targetv2.PlanResult{}, fromStatus("Plan", err)
	}
	return targetv2.PlanResult{
		Changes:  res.GetChanges(),
		Diff:     res.GetDiff(),
		Rendered: res.GetRendered(),
		Blockers: res.GetBlockers(),
	}, nil
}

func (c *Client) Apply(ctx context.Context, req targetv2.ApplyRequest) (targetv2.ApplyResult, error) {
	res, err := c.remote.Apply(ctx, &pb.ApplyRequest{
		Desired:        desiredToWire(req.Desired),
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		return targetv2.ApplyResult{}, fromStatus("Apply", err)
	}
	return targetv2.ApplyResult{
		Changed: res.GetChanged(),
		Detail:  res.GetDetail(),
		Handle:  res.GetHandle(),
	}, nil
}

func (c *Client) Observe(ctx context.Context, req targetv2.ObserveRequest) (targetv2.Observation, error) {
	res, err := c.remote.Observe(ctx, &pb.ObserveRequest{Handle: req.Handle})
	if err != nil {
		return targetv2.Observation{}, fromStatus("Observe", err)
	}
	return targetv2.Observation{
		Fingerprint: res.GetFingerprint(),
		Health: targetv2.HealthStatus{
			State:  healthFromWire(res.GetHealth().GetState()),
			Reason: res.GetHealth().GetReason(),
		},
		Meta: res.GetMeta(),
	}, nil
}

func (c *Client) Rollback(ctx context.Context, req targetv2.RollbackRequest) (targetv2.RollbackResult, error) {
	res, err := c.remote.Rollback(ctx, &pb.RollbackRequest{Handle: req.Handle})
	if err != nil {
		return targetv2.RollbackResult{}, fromStatus("Rollback", err)
	}
	return targetv2.RollbackResult{Changed: res.GetChanged(), Detail: res.GetDetail()}, nil
}

func (c *Client) Promote(ctx context.Context, req targetv2.PromoteRequest) (targetv2.PromoteResult, error) {
	res, err := c.remote.Promote(ctx, &pb.PromoteRequest{Handle: req.Handle})
	if err != nil {
		return targetv2.PromoteResult{}, fromStatus("Promote", err)
	}
	return targetv2.PromoteResult{Detail: res.GetDetail()}, nil
}

func (c *Client) DetectDrift(ctx context.Context, req targetv2.DriftRequest) (targetv2.DriftResult, error) {
	res, err := c.remote.DetectDrift(ctx, &pb.DetectDriftRequest{Desired: desiredToWire(req.Desired)})
	if err != nil {
		return targetv2.DriftResult{}, fromStatus("DetectDrift", err)
	}
	return targetv2.DriftResult{Drifted: res.GetDrifted(), Detail: res.GetDetail()}, nil
}

func (c *Client) Prune(ctx context.Context, _ targetv2.PruneRequest) (targetv2.PruneResult, error) {
	res, err := c.remote.Prune(ctx, &pb.PruneRequest{})
	if err != nil {
		return targetv2.PruneResult{}, fromStatus("Prune", err)
	}
	return targetv2.PruneResult{Removed: int(res.GetRemoved())}, nil
}
