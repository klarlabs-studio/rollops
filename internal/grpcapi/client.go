package grpcapi

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gopkg.in/yaml.v3"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/grpcapi/rollopsv1"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/servertls"
)

// Client is the CLI's daemon-mode adapter: it implements the same operation
// surface as the in-process engine, but over gRPC to a running daemon. This is
// what makes "two modes, identical command surface" hold — the CLI dispatches
// through this exactly as it would the engine.
type Client struct {
	rpc   rollopsv1.RolloutServiceClient
	token string
	conn  *grpc.ClientConn
}

// Dial connects to a daemon at addr with a bearer token. With ROLLOPS_TLS_*
// set it uses the same servertls material as rollopsd; otherwise it dials
// plaintext (the loopback dev default).
func Dial(addr, token string) (*Client, error) {
	tlsCfg, err := servertls.FromEnv()
	if err != nil {
		return nil, fmt.Errorf("grpc: tls: %w", err)
	}
	var creds credentials.TransportCredentials
	if tlsCfg == nil {
		creds = insecure.NewCredentials()
	} else {
		tc, err := tlsCfg.ClientTLS()
		if err != nil {
			return nil, fmt.Errorf("grpc: client tls: %w", err)
		}
		creds = credentials.NewTLS(tc)
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("grpc: dial %s: %w", addr, err)
	}
	return &Client{rpc: rollopsv1.NewRolloutServiceClient(conn), token: token, conn: conn}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

func (c *Client) ctx(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+c.token))
}

// Plan over gRPC.
func (c *Client) Plan(ctx context.Context, cfg *config.Config) (*engine.Plan, error) {
	y, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	r, err := c.rpc.Plan(c.ctx(ctx), &rollopsv1.PlanRequest{Config: string(y)})
	if err != nil {
		return nil, err
	}
	return &engine.Plan{
		Action: engine.PlanAction(r.GetAction()), Changed: r.GetChanged(), Summary: r.GetSummary(),
		RiskScore: r.GetRiskScore(), NeedsApproval: r.GetNeedsApproval(), Sensitive: r.GetSensitive(),
	}, nil
}

// Apply over gRPC.
func (c *Client) Apply(ctx context.Context, req engine.ApplyRequest) (*rollout.Rollout, error) {
	y, err := yaml.Marshal(req.Config)
	if err != nil {
		return nil, err
	}
	r, err := c.rpc.Apply(c.ctx(ctx), &rollopsv1.ApplyRequest{Config: string(y)})
	if err != nil {
		return nil, err
	}
	return &rollout.Rollout{ID: r.GetId(), Phase: rollout.Phase(r.GetPhase()), TargetRef: r.GetTarget(), RiskScore: r.GetRiskScore()}, nil
}

// Status over gRPC.
func (c *Client) Status(ctx context.Context, id string) (rollout.Rollout, error) {
	r, err := c.rpc.Status(c.ctx(ctx), &rollopsv1.StatusRequest{Id: id})
	if err != nil {
		return rollout.Rollout{}, err
	}
	return rollout.Rollout{
		ID: r.GetId(), Phase: rollout.Phase(r.GetPhase()), TargetRef: r.GetTarget(), Strategy: rollout.Strategy(r.GetStrategy()),
		StepIndex: int(r.GetStepIndex()), StepTotal: int(r.GetStepTotal()), StepWeight: int(r.GetStepWeight()),
		Note: r.GetNote(), RiskScore: r.GetRiskScore(),
		Initiator: rollout.Identity{Kind: r.GetActorKind(), Name: r.GetActorName()},
	}, nil
}

// Promote marks a verified rollout promoted over gRPC, gated on the post-deploy
// checks. force overrides a failing gate.
func (c *Client) Promote(ctx context.Context, id string, force bool) (rollout.Rollout, error) {
	r, err := c.rpc.Promote(c.ctx(ctx), &rollopsv1.RolloutActionRequest{Id: id, Force: force})
	if err != nil {
		return rollout.Rollout{}, err
	}
	return rollout.Rollout{
		ID: r.GetId(), Phase: rollout.Phase(r.GetPhase()), TargetRef: r.GetTarget(), Note: r.GetNote(),
	}, nil
}

// Verify dry-runs the post-deploy gate over gRPC and returns the report. A
// failing gate comes back as a report with OK=false, not an error.
func (c *Client) Verify(ctx context.Context, id string) (engine.VerifyReport, error) {
	r, err := c.rpc.Verify(c.ctx(ctx), &rollopsv1.RolloutActionRequest{Id: id})
	if err != nil {
		return engine.VerifyReport{}, err
	}
	gates := make([]engine.GateResult, 0, len(r.GetGates()))
	for _, g := range r.GetGates() {
		gates = append(gates, engine.GateResult{Gate: g.GetGate(), Status: g.GetStatus(), Detail: g.GetDetail()})
	}
	return engine.VerifyReport{
		RolloutID: r.GetId(), TargetRef: r.GetTarget(), Phase: r.GetPhase(),
		OK: r.GetOk(), Reason: r.GetReason(), Gates: gates,
	}, nil
}

// Approve approves a rollout awaiting approval over gRPC.
func (c *Client) Approve(ctx context.Context, id string) (rollout.Rollout, error) {
	return c.rolloutAction(ctx, c.rpc.Approve, id)
}

// Reject rejects a rollout awaiting approval over gRPC.
func (c *Client) Reject(ctx context.Context, id string) (rollout.Rollout, error) {
	return c.rolloutAction(ctx, c.rpc.Reject, id)
}

// Pause holds an in-flight canary over gRPC.
func (c *Client) Pause(ctx context.Context, id string) (rollout.Rollout, error) {
	return c.rolloutAction(ctx, c.rpc.Pause, id)
}

// Resume continues an operator-paused canary over gRPC.
func (c *Client) Resume(ctx context.Context, id string) (rollout.Rollout, error) {
	return c.rolloutAction(ctx, c.rpc.Resume, id)
}

// Abort stops an in-flight canary and rolls it back over gRPC.
func (c *Client) Abort(ctx context.Context, id string) (rollout.Rollout, error) {
	return c.rolloutAction(ctx, c.rpc.Abort, id)
}

// Freeze toggles the emergency kill-switch over gRPC.
func (c *Client) Freeze(ctx context.Context, on bool, reason string) (bool, string, error) {
	r, err := c.rpc.Freeze(c.ctx(ctx), &rollopsv1.FreezeRequest{Active: on, Reason: reason})
	if err != nil {
		return false, "", err
	}
	return r.GetActive(), r.GetReason(), nil
}

// FleetStatus aggregates latest-per-target phases for a RolloutSet-style prefix.
func (c *Client) FleetStatus(ctx context.Context, filter string) (engine.FleetReport, error) {
	r, err := c.rpc.FleetStatus(c.ctx(ctx), &rollopsv1.FleetStatusRequest{Filter: filter})
	if err != nil {
		return engine.FleetReport{}, err
	}
	members := make([]engine.FleetMember, 0, len(r.GetMembers()))
	for _, m := range r.GetMembers() {
		members = append(members, engine.FleetMember{ID: m.GetId(), Target: m.GetTarget(), Phase: rollout.Phase(m.GetPhase())})
	}
	return engine.FleetReport{
		Name: r.GetName(), Total: int(r.GetTotal()), Promoted: int(r.GetPromoted()),
		Active: int(r.GetActive()), Degraded: int(r.GetDegraded()), Awaiting: int(r.GetAwaiting()),
		Members: members,
	}, nil
}

func (c *Client) rolloutAction(ctx context.Context, rpc func(context.Context, *rollopsv1.RolloutActionRequest, ...grpc.CallOption) (*rollopsv1.RolloutActionResponse, error), id string) (rollout.Rollout, error) {
	r, err := rpc(c.ctx(ctx), &rollopsv1.RolloutActionRequest{Id: id})
	if err != nil {
		return rollout.Rollout{}, err
	}
	return rollout.Rollout{ID: r.GetId(), Phase: rollout.Phase(r.GetPhase()), TargetRef: r.GetTarget(), Note: r.GetNote()}, nil
}

// RollbackLast rolls a target back over gRPC. force overrides the
// backward-compatibility gate.
func (c *Client) RollbackLast(ctx context.Context, targetRef string, force bool) (rollout.Rollout, error) {
	r, err := c.rpc.Rollback(c.ctx(ctx), &rollopsv1.RollbackRequest{Target: targetRef, Force: force})
	if err != nil {
		return rollout.Rollout{}, err
	}
	return rollout.Rollout{ID: r.GetId(), Phase: rollout.Phase(r.GetPhase()), TargetRef: r.GetTarget()}, nil
}
