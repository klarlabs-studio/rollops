package grpcapi

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gopkg.in/yaml.v3"

	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/grpcapi/rollopsv1"
	"go.klarlabs.de/rollops/internal/rollout"
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

// Dial connects to a daemon at addr with a bearer token.
func Dial(addr, token string) (*Client, error) {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
	return &engine.Plan{Action: engine.PlanAction(r.GetAction()), Changed: r.GetChanged(), Summary: r.GetSummary()}, nil
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
	return &rollout.Rollout{ID: r.GetId(), Phase: rollout.Phase(r.GetPhase()), TargetRef: r.GetTarget()}, nil
}

// Status over gRPC.
func (c *Client) Status(ctx context.Context, id string) (rollout.Rollout, error) {
	r, err := c.rpc.Status(c.ctx(ctx), &rollopsv1.StatusRequest{Id: id})
	if err != nil {
		return rollout.Rollout{}, err
	}
	return rollout.Rollout{ID: r.GetId(), Phase: rollout.Phase(r.GetPhase()), TargetRef: r.GetTarget(), Strategy: rollout.Strategy(r.GetStrategy())}, nil
}

// Promote is not yet exposed over gRPC; use the daemon UI or a local engine.
func (c *Client) Promote(ctx context.Context, id string) (rollout.Rollout, error) {
	return rollout.Rollout{}, fmt.Errorf("promote is not available in daemon mode yet; use the UI or a one-shot engine")
}

// RollbackLast rolls a target back over gRPC.
func (c *Client) RollbackLast(ctx context.Context, targetRef string) (rollout.Rollout, error) {
	r, err := c.rpc.Rollback(c.ctx(ctx), &rollopsv1.RollbackRequest{Target: targetRef})
	if err != nil {
		return rollout.Rollout{}, err
	}
	return rollout.Rollout{ID: r.GetId(), Phase: rollout.Phase(r.GetPhase()), TargetRef: r.GetTarget()}, nil
}
