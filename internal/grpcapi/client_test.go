package grpcapi

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/cli"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/rollout"
)

// The gRPC client must present the same surface the CLI drives in-process.
var _ cli.Operations = (*Client)(nil)

func TestClient_RoundTripThroughGRPC(t *testing.T) {
	rpc := dialBuf(t) // in-process gRPC server with the "op" role for t-felix
	c := &Client{rpc: rpc, token: "t-felix"}
	ctx := context.Background()

	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}

	p, err := c.Plan(ctx, cfg)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Summary == "" {
		t.Error("empty plan summary over gRPC")
	}
	r, err := c.Apply(ctx, engine.ApplyRequest{Config: cfg})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.ID != "ro-grpc" || string(r.Phase) != "verifying" {
		t.Errorf("apply over gRPC = %+v", r)
	}
	s, err := c.Status(ctx, "ro-grpc")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if string(s.Phase) != "verifying" {
		t.Errorf("status over gRPC = %+v", s)
	}
}

func TestClient_RollbackThroughGRPC(t *testing.T) {
	n := 0
	rpc := dialBufWithID(t, func() string {
		n++
		return "ro-client-" + string(rune('0'+n))
	})
	c := &Client{rpc: rpc, token: "t-felix"}
	ctx := context.Background()

	cfg, err := config.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(ctx, engine.ApplyRequest{Config: cfg}); err != nil {
		t.Fatalf("Apply first: %v", err)
	}
	cfg2, err := config.Load([]byte(strings.Replace(cfgYAML, "x: 1", "x: 2", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Apply(ctx, engine.ApplyRequest{Config: cfg2}); err != nil {
		t.Fatalf("Apply second: %v", err)
	}
	rb, err := c.RollbackLast(ctx, "demo/prod/app")
	if err != nil {
		t.Fatalf("RollbackLast: %v", err)
	}
	if rb.Phase != rollout.PhaseRolledBack || rb.TargetRef != "demo/prod/app" {
		t.Errorf("rollback over gRPC = %+v", rb)
	}
}

func TestClient_RBACPropagates(t *testing.T) {
	rpc := dialBuf(t)
	c := &Client{rpc: rpc, token: "t-bot"} // viewer
	cfg, _ := config.Load([]byte(cfgYAML))
	if _, err := c.Apply(context.Background(), engine.ApplyRequest{Config: cfg}); err == nil {
		t.Fatal("viewer apply must be denied through the gRPC client")
	}
}
